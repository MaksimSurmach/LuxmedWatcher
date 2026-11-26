package bot

import (
	"context"
	"errors"
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
		return command != "cancel"
	default:
		return false
	}
}

// Telegram acknowledgments can block on the network. Cancel locally as soon
// as an authorized Chrome choice arrives, then confirm it in storage.
func (b *Bot) cancelPendingChromeLogin(ctx context.Context, telegramID int64, action string) {
	if (action != "connect:chrome" && action != "connect:open") || b.cfg.Browsers[telegramID] == nil {
		return
	}
	user, err := b.db.UserByTelegramID(ctx, telegramID)
	if err != nil || !user.IsActive() {
		return
	}
	pending := b.getFlow(user.ID)
	if pending != nil && pending.Kind == flowAccountCheck {
		pending.cancel()
	}
}

func (b *Bot) submitLogin(parent context.Context, user domain.User, creds luxmed.Credentials) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	pending := &flow{Kind: flowAccountCheck, cancel: cancel}
	b.setFlow(user.ID, pending)
	b.workers.Go(func() {
		defer cancel()
		err := b.saveLogin(ctx, user, creds)
		canceled := parent.Err() != nil || errors.Is(ctx.Err(), context.Canceled) ||
			errors.Is(err, storage.ErrCredentialsChanged)
		var retry *flow
		if err != nil && !canceled {
			retry = &flow{Kind: flowAccountPass, Login: creds.Login}
		}
		if !b.completeLogin(user.ID, pending, retry) || canceled {
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
func (b *Bot) completeLogin(userID int64, pending, retry *flow) bool {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	if b.flowState[userID] != pending {
		return false
	}
	if retry == nil {
		delete(b.flowState, userID)
	} else {
		b.flowState[userID] = retry
	}
	return true
}

func (b *Bot) saveLogin(ctx context.Context, user domain.User, creds luxmed.Credentials) error {
	if b.runner != nil {
		return b.runner.SaveCredentials(ctx, user, creds)
	}
	// A bot without a runner is used by the standalone bot tests.
	if err := b.newClient(false).Authenticate(ctx, creds); err != nil {
		return err
	}
	_, err := b.db.SaveCredentials(ctx, b.cryptor, user, creds)
	return err
}
