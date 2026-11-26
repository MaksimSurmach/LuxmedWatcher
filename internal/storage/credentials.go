package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
)

var ErrCredentialsChanged = errors.New("account changed during sign-in")

func (db *DB) SaveCredentials(ctx context.Context, key *security.Cryptor, owner domain.User, creds security.Credentials) (string, error) {
	sealed, err := key.SealCredentials(owner.ID, creds)
	if err != nil {
		return "", err
	}
	// Verification calls LUX MED outside the transaction. A later opt-out or
	// logout takes precedence even when no credentials existed before that call.
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	err = requireOneRow(tx.ExecContext(ctx, `
		UPDATE users SET credential_revision=credential_revision+1
		WHERE id=$1 AND credential_revision=$2 AND credential_save_revision=$3 AND status=$4 AND role <> ''
	`, owner.ID, owner.CredentialRevision, owner.CredentialSaveRevision, domain.UserStatusActive))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCredentialsChanged
	}
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := upsertLuxMedAccount(ctx, tx, domain.LuxMedAccount{
		UserID: owner.ID, EncryptedPassword: sealed, UseSavedLogin: true,
		LastLoginAt: &now, Status: domain.AccountStatusActive,
	}); err != nil {
		return "", err
	}
	if err := db.finalizeLuxMedAccountSave(ctx, tx, owner.ID, false); err != nil {
		return "", err
	}
	if err := db.setModeAccessStatus(ctx, tx, owner.ID, LuxMedAccessConnected); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return sealed, nil
}

// Hold the user row until the result commits, ordering it against a mode change.
func (db *DB) withCredentialRevision(ctx context.Context, owner domain.User, write func(*sql.Tx) error) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const current = ` WHERE id=$1 AND credential_revision=$2 AND status=$3 AND role <> ''`
	if db.postgres {
		var revision int64
		err = tx.QueryRowContext(ctx, `SELECT credential_revision FROM users`+current+` FOR UPDATE`,
			owner.ID, owner.CredentialRevision, domain.UserStatusActive).Scan(&revision)
	} else {
		err = requireOneRow(tx.ExecContext(ctx, `UPDATE users SET credential_revision=credential_revision`+current,
			owner.ID, owner.CredentialRevision, domain.UserStatusActive))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCredentialsChanged
	}
	if err != nil {
		return err
	}
	if err := write(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) setModeAccessStatus(ctx context.Context, tx *sql.Tx, userID int64, status LuxMedAccessStatus) error {
	verification, err := luxMedReconnectVerification(ctx, tx, userID)
	if err != nil {
		return err
	}
	requiresTerms := verification.RequiresTerms || verification.HasSavedWatches
	if requiresTerms {
		status = LuxMedAccessPending
	}
	return setLuxMedAccessStatus(ctx, tx, userID, status, requiresTerms)
}

// Opening Chrome cancels a pending password save while preserving in-flight Chrome searches.
func (db *DB) ConfirmBrowserLogin(ctx context.Context, owner domain.User) error {
	err := requireOneRow(db.sql.ExecContext(ctx, `
		UPDATE users SET credential_save_revision=credential_save_revision+1
		WHERE id=$1 AND credential_revision=$2 AND status=$3 AND role <> ''
			AND NOT EXISTS (SELECT 1 FROM luxmed_accounts WHERE user_id=$1 AND use_saved_login)
	`, owner.ID, owner.CredentialRevision, domain.UserStatusActive))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCredentialsChanged
	}
	return err
}

// ChooseChromeDuringPasswordSave fences an attempt that began in Chrome mode.
// If it committed first, undo only that revision and restore any migrated row.
func (db *DB) ChooseChromeDuringPasswordSave(ctx context.Context, owner domain.User, previous domain.LuxMedAccount) error {
	if previous.UseSavedLogin || (previous.UserID != 0 && previous.UserID != owner.ID) {
		return ErrCredentialsChanged
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int64
	err = tx.QueryRowContext(ctx, `
		UPDATE users SET credential_save_revision=credential_save_revision+1
		WHERE id=$1 AND credential_save_revision=$3 AND status=$4 AND role <> ''
			AND credential_revision IN ($2, $5)
		RETURNING credential_revision
	`, owner.ID, owner.CredentialRevision, owner.CredentialSaveRevision,
		domain.UserStatusActive, owner.CredentialRevision+1).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCredentialsChanged
	}
	if err != nil {
		return err
	}
	// Read the account after taking the user row lock. In PostgreSQL a waiter
	// can see the updated user row before a new statement sees the account write.
	var savedMode bool
	err = tx.QueryRowContext(ctx, `
		SELECT use_saved_login FROM luxmed_accounts WHERE user_id=$1
	`, owner.ID).Scan(&savedMode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if savedMode != (revision == owner.CredentialRevision+1) {
		return ErrCredentialsChanged
	}
	if revision == owner.CredentialRevision {
		return tx.Commit()
	}
	if previous.UserID == owner.ID {
		if err := upsertLuxMedAccount(ctx, tx, previous); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM luxmed_accounts WHERE user_id=$1 AND use_saved_login`, owner.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET credential_revision=credential_revision+1 WHERE id=$1`, owner.ID); err != nil {
		return err
	}
	if err := db.finalizeLuxMedAccountSave(ctx, tx, owner.ID, false); err != nil {
		return err
	}
	if err := db.setModeAccessStatus(ctx, tx, owner.ID, LuxMedAccessUnchecked); err != nil {
		return err
	}
	return tx.Commit()
}

// Earlier versions stored the login in plaintext and encrypted only the password.
// Preserve the previous mode in installations with a browser configured.
func (db *DB) EncryptLegacyLogins(ctx context.Context, key *security.Cryptor) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT user_id, login, encrypted_password FROM luxmed_accounts WHERE login <> ''")
	if err != nil {
		return err
	}
	defer rows.Close()
	type change struct {
		id                  int64
		oldPassword, sealed string
	}
	var changes []change
	for rows.Next() {
		var login string
		var item change
		if err := rows.Scan(&item.id, &login, &item.oldPassword); err != nil {
			return err
		}
		password, err := key.Decrypt(item.oldPassword)
		if err != nil {
			return err
		}
		item.sealed, err = key.SealCredentials(item.id, security.Credentials{Login: login, Password: password})
		if err != nil {
			return err
		}
		changes = append(changes, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range changes {
		if _, err := tx.ExecContext(ctx, "UPDATE luxmed_accounts SET login='', encrypted_password=$1 WHERE user_id=$2 AND encrypted_password=$3 AND login <> ''", item.sealed, item.id, item.oldPassword); err != nil {
			return err
		}
	}
	return tx.Commit()
}
