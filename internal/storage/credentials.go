package storage

import (
	"context"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
)

func (db *DB) SaveCredentials(ctx context.Context, key *security.Cryptor, userID int64, creds security.Credentials) error {
	sealed, err := key.SealCredentials(userID, creds)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
		UserID: userID, EncryptedPassword: sealed, UseSavedLogin: true,
		LastLoginAt: &now, Status: domain.AccountStatusActive,
	})
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
