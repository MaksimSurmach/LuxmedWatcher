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

func loginExitCommand(command string) bool {
	return command == "cancel" || command == "logout_luxmed" || command == "connect"
}

func (b *Bot) messageCommand(message *tgbotapi.Message, state *flow) string {
	command := message.Command()
	checkingPassword := state != nil && state.Kind == flowAccountCheck
	if !loginExitCommand(command) && !checkingPassword {
		return command
	}
	// An entity can cover only a prefix of a password.
	if message.Text == "/"+command {
		return command
	}
	if username := b.api.Self.UserName; username != "" && strings.EqualFold(message.Text, "/"+command+"@"+username) {
		return command
	}
	return ""
}

func (state *flow) receivesCredential(command string) bool {
	if state == nil {
		return false
	}
	switch state.Kind {
	case flowAccountLogin:
		return command == ""
	case flowAccountPass:
		return !loginExitCommand(command)
	case flowAccountCheck:
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

type loginNotice struct {
	user  domain.User
	key   string
	retry *flow
	saved bool
}

func (b *Bot) deliverLoginNotice(notice *loginNotice) *flow {
	user := notice.user
	markup := mainKeyboard(b.catalog, user.Locale)
	if notice.retry != nil {
		markup = cancelKeyboard(b.catalog, user.Locale)
	}
	if b.send(user, b.catalog.T(user.Locale, notice.key), markup) {
		return notice.retry
	}
	return &flow{Kind: flowAccountCheck, notice: notice}
}

func (b *Bot) retryLoginNotice(telegramID int64, state *flow) {
	if state.notice != nil && b.getFlow(telegramID) == state {
		next := b.deliverLoginNotice(state.notice)
		b.finishLogin(telegramID, state, next, false)
	}
}

type credentialDeleteWarning struct {
	user    domain.User
	flow    *flow
	key     string // Set after the previous login replies finish, under flowMu.
	sending bool   // Protected by flowMu.
}

func (b *Bot) stopLoginAfterDeleteFailure(ctx context.Context, telegramID int64, previous *flow, locale string) {
	if previous.warningPending {
		return
	}
	state := &flow{Kind: flowAccountCheck, warningPending: true}
	warning := &credentialDeleteWarning{
		user: domain.User{TelegramChatID: telegramID, Locale: locale}, flow: state,
	}
	verification := previous.verification
	b.flowMu.Lock()
	// Cancel before waiting for any already-started Telegram response.
	if previous.cancel != nil {
		previous.cancel()
	}
	b.replaceCredentialFlowLocked(telegramID, state)
	b.deleteWarnings[telegramID] = append(b.deleteWarnings[telegramID], warning)
	var cancelDone <-chan struct{}
	if verification != nil {
		cancelDone = verification.cancelDone
	}
	b.flowMu.Unlock()

	warn := func(saved bool) {
		key := "flow.account.delete_failed"
		if saved {
			key = "flow.account.delete_failed_saved"
		}
		b.flowMu.Lock()
		warning.key = key
		b.flowMu.Unlock()
		if ctx.Err() != nil {
			b.finishLogin(telegramID, state, nil, false)
			return
		}
		b.deliverDeleteWarnings(telegramID, warning)
	}
	if verification == nil {
		warn(previous.notice != nil && previous.notice.saved)
		return
	}
	b.workers.Go(func() {
		<-verification.done
		// A cancellation reply may also have started before the deletion failed.
		if cancelDone != nil {
			<-cancelDone
		}
		warn(verification.err == nil)
	})
}

// Capture the retry before handling an update so a new warning gets one attempt
// now and waits for another interaction if Telegram rejects it.
func (b *Bot) readyDeleteWarning(telegramID int64) *credentialDeleteWarning {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	queue := b.deleteWarnings[telegramID]
	if len(queue) != 0 && queue[0].key != "" && !queue[0].sending {
		return queue[0]
	}
	return nil
}

func (b *Bot) deliverDeleteWarnings(telegramID int64, warning *credentialDeleteWarning) {
	for warning != nil {
		b.flowMu.Lock()
		queue := b.deleteWarnings[telegramID]
		if len(queue) == 0 || queue[0] != warning || warning.key == "" || warning.sending {
			b.flowMu.Unlock()
			return
		}
		warning.sending = true
		key := warning.key
		b.flowMu.Unlock()

		sent := b.send(warning.user, b.catalog.T(warning.user.Locale, key), nil)

		b.flowMu.Lock()
		warning.sending = false
		if !sent {
			b.flowMu.Unlock()
			return
		}
		queue = b.deleteWarnings[telegramID]
		if b.credentialFlows[telegramID] == warning.flow {
			b.replaceCredentialFlowLocked(telegramID, nil)
		}
		queue[0] = nil
		if len(queue) == 1 {
			delete(b.deleteWarnings, telegramID)
			b.flowMu.Unlock()
			return
		}
		b.deleteWarnings[telegramID] = queue[1:]
		warning = queue[1]
		b.flowMu.Unlock()
	}
}

type loginVerification struct {
	user       domain.User
	done       chan struct{}
	err        error         // Published by closing done.
	cancelDone chan struct{} // Protected by flowMu.
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
	verification := &loginVerification{user: user, done: make(chan struct{})}
	pending := &flow{Kind: flowAccountCheck, cancel: cancel, verification: verification}
	b.setFlow(user.TelegramUserID, pending)
	b.workers.Go(func() {
		defer close(verification.done)
		defer cancel()
		var retry *flow
		defer func() {
			if parent.Err() != nil {
				retry = nil
			}
			b.finishLogin(user.TelegramUserID, pending, retry, false)
		}()
		err := b.saveLogin(user, creds, attempt)
		verification.err = err
		if parent.Err() != nil || errors.Is(ctx.Err(), context.Canceled) ||
			!b.ownsLogin(user.TelegramUserID, pending, false) {
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
			retry = b.deliverLoginNotice(&loginNotice{
				user: user, key: key, retry: &flow{Kind: flowAccountPass, Login: creds.Login},
			})
			return
		}
		key := "flow.account.saved"
		if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err == nil && required {
			key = "flow.account.verification_pending"
		}
		if b.ownsLogin(user.TelegramUserID, pending, false) {
			retry = b.deliverLoginNotice(&loginNotice{user: user, key: key, saved: true})
		}
	})
}

// cancelLogin cancels the chat's exact verification before any database access.
func (b *Bot) cancelLogin(ctx context.Context, telegramID int64, pending *flow) bool {
	if pending == nil || pending.verification == nil {
		return false
	}
	verification := pending.verification
	b.flowMu.Lock()
	if b.credentialFlows[telegramID] != pending {
		b.flowMu.Unlock()
		return false
	}
	if verification.cancelDone != nil {
		b.flowMu.Unlock()
		return true
	}
	done := make(chan struct{})
	verification.cancelDone = done
	pending.cancel()
	b.flowMu.Unlock()

	b.workers.Go(func() {
		defer close(done)
		var next *flow
		defer func() {
			if ctx.Err() != nil {
				next = nil
			}
			b.finishLogin(telegramID, pending, next, true)
		}()
		select {
		case <-ctx.Done():
			return
		case <-verification.done:
		}
		if ctx.Err() != nil || !b.ownsLogin(telegramID, pending, true) {
			return
		}
		key := "flow.account.canceled"
		if verification.err == nil {
			key = "flow.account.already_saved"
		}
		next = b.deliverLoginNotice(&loginNotice{user: verification.user, key: key, saved: verification.err == nil})
	})
	return true
}

func (b *Bot) ownsLogin(telegramID int64, pending *flow, canceled bool) bool {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	return b.credentialFlows[telegramID] == pending && (pending.verification.cancelDone != nil) == canceled
}

// Keep scrubbing input until the result (or cancellation result) has been sent.
func (b *Bot) finishLogin(telegramID int64, pending, retry *flow, canceled bool) {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	if b.credentialFlows[telegramID] != pending || (pending.verification != nil && (pending.verification.cancelDone != nil) != canceled) {
		return
	}
	b.replaceCredentialFlowLocked(telegramID, retry)
}

func (b *Bot) clearCredentialFlow(telegramID int64) bool {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	return b.replaceCredentialFlowLocked(telegramID, nil)
}

func (b *Bot) replaceCredentialFlowLocked(telegramID int64, next *flow) bool {
	previous := b.credentialFlows[telegramID]
	if previous != nil && previous != next && previous.cancel != nil {
		previous.cancel()
	}
	if next == nil {
		delete(b.credentialFlows, telegramID)
	} else {
		b.credentialFlows[telegramID] = next
	}
	return previous != nil
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
