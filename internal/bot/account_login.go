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
	case flowAccountPass, flowAccountCheck:
		command := message.Command()
		return command != "cancel" && command != "logout_luxmed" && command != "connect"
	default:
		return false
	}
}

func chromeButton(action string) (revision *int64, switchMode, ok bool) {
	if action == "connect:open" || action == "connect:chrome" {
		return nil, false, true
	}
	name, value, found := strings.Cut(action, ":")
	if !found || name != "connect" {
		return nil, false, false
	}
	mode, number, found := strings.Cut(value, ":")
	if !found || (mode != "chrome" && mode != "switch") {
		return nil, false, false
	}
	rev, err := strconv.ParseInt(number, 10, 64)
	if err != nil || rev < 0 {
		return nil, false, false
	}
	return &rev, mode == "switch", true
}

func forgetButton(action string) (int64, bool) {
	raw, found := strings.CutPrefix(action, "connect:forget:")
	if !found {
		return 0, false
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	return revision, err == nil && revision >= 0
}

// Capture the choice before reading the database or acknowledging the callback.
func (b *Bot) selectChrome(telegramID int64, action string) *storage.ChromeChoice {
	revision, switchMode, ok := chromeButton(action)
	if !ok || b.cfg.Browsers[telegramID] == nil {
		return nil
	}
	return b.db.SelectBrowserLoginAtRevision(telegramID, revision, switchMode)
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
