package bot

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

func (state *flow) receivesCredential(message *tgbotapi.Message) bool {
	if state == nil {
		return false
	}
	switch state.Kind {
	case flowAccountLogin:
		return !message.IsCommand()
	case flowAccountPass:
		command := message.Command()
		return command != "cancel" && command != "logout_luxmed" && command != "connect"
	case flowAccountCheck:
		command := message.Command()
		if command == "logout_luxmed" || command == "invite" {
			return false
		}
		for _, registered := range registeredCommands {
			if command == registered.name {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func credentialAction(mode string, revisions domain.CredentialRevisions) string {
	return "connect:" + mode + ":" + strconv.FormatInt(revisions.CredentialRevision, 10) +
		":" + strconv.FormatInt(revisions.CredentialSaveRevision, 10)
}

func credentialButton(action string) (mode string, revisions *domain.CredentialRevisions) {
	tail, valid := strings.CutPrefix(action, "connect:")
	if !valid {
		return "", nil
	}
	mode, raw, _ := strings.Cut(tail, ":")
	switch mode {
	case "chrome", "open", "switch", "forget":
	default:
		return "", nil
	}
	modeRevision, saveRevision, _ := strings.Cut(raw, ":")
	revision, modeErr := strconv.ParseInt(modeRevision, 10, 64)
	save, saveErr := strconv.ParseInt(saveRevision, 10, 64)
	if modeErr != nil || saveErr != nil || revision < 0 || save < 0 {
		return mode, nil
	}
	return mode, &domain.CredentialRevisions{CredentialRevision: revision, CredentialSaveRevision: save}
}

// Capture the choice before reading the database or acknowledging the callback.
func (b *Bot) selectChrome(telegramID int64, action string) *storage.ChromeChoice {
	mode, revisions := credentialButton(action)
	if revisions == nil || (mode != "chrome" && mode != "switch") || b.cfg.Browsers[telegramID] == nil {
		return nil
	}
	return b.db.SelectBrowserLogin(telegramID, "", *revisions, mode == "switch")
}

func (b *Bot) submitLogin(parent context.Context, user domain.User, creds luxmed.Credentials) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	attempt, err := b.db.StartCredentialAttempt(ctx, user, "")
	if err != nil {
		cancel()
		key := "errors.generic"
		if errors.Is(err, storage.ErrCredentialAttemptBusy) {
			key = "watch.browser_busy"
		}
		b.send(user, b.catalog.T(user.Locale, key), cancelKeyboard(b.catalog, user.Locale))
		return
	}
	pending := &flow{Kind: flowAccountCheck, cancel: cancel}
	b.setFlow(user.TelegramUserID, pending)
	b.workers.Go(func() {
		defer cancel()
		err := b.saveLogin(user, creds, attempt)
		canceled := parent.Err() != nil || errors.Is(ctx.Err(), context.Canceled) ||
			errors.Is(err, storage.ErrCredentialsChanged)
		var retry *flow
		if err != nil && !canceled {
			retry = &flow{Kind: flowAccountPass, Login: creds.Login}
		}
		if !b.completeLogin(user.TelegramUserID, pending, retry) || canceled {
			return
		}
		if err != nil {
			key := "errors.generic"
			switch {
			case errors.Is(err, luxmed.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
				key = "errors.luxmed_unavailable"
			case errors.Is(err, luxmed.ErrAuthentication):
				key = "errors.auth_failed"
			case errors.Is(err, luxmed.ErrBrowserBusy):
				key = "watch.browser_busy"
			}
			b.send(user, b.catalog.T(user.Locale, key), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		key := "flow.account.saved"
		if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err == nil && required {
			key = "flow.account.verification_pending"
		}
		b.send(user, b.catalog.T(user.Locale, key), mainKeyboard(b.catalog, user.Locale))
	})
}

// Completion changes the flow only if it still owns the verification slot.
func (b *Bot) completeLogin(telegramID int64, pending, retry *flow) bool {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	if b.flowState[telegramID] != pending {
		return false
	}
	if retry == nil {
		delete(b.flowState, telegramID)
	} else {
		b.flowState[telegramID] = retry
	}
	return true
}

func (b *Bot) saveLogin(user domain.User, creds luxmed.Credentials, attempt *storage.CredentialAttempt) error {
	if b.runner != nil {
		return b.runner.VerifyCredentialAttempt(user, creds, attempt)
	}
	// A bot without a runner is used by the standalone bot tests.
	defer attempt.Finish()
	ctx := attempt.Context()
	if err := b.db.PrepareCredentialAttempt(ctx, b.cryptor, user, creds, attempt); err != nil {
		return err
	}
	if err := b.newClient(false).Authenticate(ctx, creds); err != nil {
		return err
	}
	_, err := b.db.SaveCredentialAttempt(ctx, attempt)
	return err
}
