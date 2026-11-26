package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
)

const reconnectForbiddenLimit = 4

func (b *Bot) handleTelegramUnblock(ctx context.Context, update *tgbotapi.ChatMemberUpdated) {
	if b.runner == nil || !privateChat(&update.Chat, &update.From) ||
		update.OldChatMember.Status != "kicked" || update.NewChatMember.Status != "member" ||
		update.NewChatMember.User == nil || update.NewChatMember.User.ID != b.api.Self.ID {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := b.resumeAfterTelegramUnblock(ctx, update.From.ID); err != nil && !errors.Is(err, luxmed.ErrBrowserBusy) {
		b.logger.Warn("accelerate reconnect warning after Telegram unblock", "err", err)
	}
}

func (b *Bot) resumeAfterTelegramUnblock(ctx context.Context, telegramID int64) error {
	user, err := b.db.UserByTelegramID(ctx, telegramID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !user.IsActive()) {
		return nil
	}
	if err != nil {
		return err
	}
	return b.runner.ResumeLuxMedReconnectDelivery(ctx, user.ID)
}

func (b *Bot) NotifyLuxMedReconnect(ctx context.Context, user domain.User) error {
	if !user.IsActive() || user.TelegramChatID <= 0 || user.TelegramChatID != user.TelegramUserID {
		return errors.New("reconnect recipient has no private chat")
	}
	button := b.actionButton(user, "button.connect_luxmed", "account")
	if b.cfg.MiniApp != nil && b.cfg.Browsers[user.TelegramUserID] != nil {
		button = newMiniAppButton(button.Text, b.dashboardURL(user, "settings"))
	}
	msg := tgbotapi.NewMessage(user.TelegramChatID, b.catalog.T(user.Locale, "notification.search_paused"))
	msg.ReplyMarkup = map[string][][]miniAppButton{"inline_keyboard": {{button}}}
	if ctx.Err() != nil {
		return &scheduler.ReconnectDeliveryRejected{BeforeSend: true}
	}
	api := *b.api
	client := &reconnectHTTP{HTTPClient: api.Client, ctx: ctx}
	api.Client = client
	_, err := api.Request(msg)
	if err == nil && (client.status < http.StatusBadRequest || client.status >= http.StatusInternalServerError) {
		return nil
	}
	rejected, apiRejected := errors.AsType[*tgbotapi.Error](err)
	code, delay := client.status, client.retryAfter
	if apiRejected {
		// A 4xx HTTP response is decisive even if its body cannot be decoded.
		if code < http.StatusBadRequest || code >= http.StatusInternalServerError {
			code = rejected.Code
		}
		if rejected.Code == code {
			delay = max(delay, time.Duration(rejected.RetryAfter)*time.Second)
		}
	}
	switch {
	case code == http.StatusTooManyRequests:
		return &scheduler.ReconnectDeliveryRejected{RetryAfter: delay}
	case code == http.StatusUnauthorized || code == http.StatusBadRequest:
		return &scheduler.ReconnectDeliveryRejected{RetryAfter: max(time.Hour, delay)}
	case code == http.StatusForbidden:
		// The user may unblock the bot while the reconnect episode is still open.
		return &scheduler.ReconnectDeliveryRejected{RetryAfter: max(6*time.Hour, delay), ForbiddenLimit: reconnectForbiddenLimit}
	case code >= http.StatusBadRequest && code < http.StatusInternalServerError:
		return errors.New("telegram permanently rejected reconnect warning")
	case apiRejected && code >= http.StatusInternalServerError:
		return &scheduler.ReconnectDeliveryRejected{RetryAfter: delay}
	}
	if client.failedBeforeConnection {
		return &scheduler.ReconnectDeliveryRejected{BeforeSend: true}
	}
	return errors.New("telegram reconnect delivery outcome unknown")
}

type reconnectHTTP struct {
	tgbotapi.HTTPClient
	ctx                    context.Context
	failedBeforeConnection bool
	status                 int
	retryAfter             time.Duration
}

func (c *reconnectHTTP) Do(req *http.Request) (*http.Response, error) {
	var connected atomic.Bool
	ctx := httptrace.WithClientTrace(c.ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	})
	res, err := c.HTTPClient.Do(req.WithContext(ctx))
	if res != nil {
		c.status = res.StatusCode
		if value := res.Header.Get("Retry-After"); value != "" {
			if seconds, err := strconv.ParseUint(value, 10, 31); err == nil {
				c.retryAfter = time.Duration(seconds) * time.Second
			} else if date, err := http.ParseTime(value); err == nil {
				c.retryAfter = max(0, time.Until(date))
			}
		}
	}
	c.failedBeforeConnection = err != nil && !connected.Load()
	return res, err
}

func (b *Bot) checkAccount(ctx context.Context, user domain.User) error {
	if b.cfg.Browsers == nil {
		_, err := b.db.LuxMedAccount(ctx, user.ID)
		return err
	}
	if b.runner != nil {
		_, err := b.runner.CheckBrowserSession(ctx, user)
		return err
	}
	browser := b.cfg.Browsers[user.TelegramUserID]
	if browser == nil {
		return luxmed.ErrAuthentication
	}
	return browser.Check(ctx)
}

func accountCheckMessage(err error) string {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "errors.no_account"
	case errors.Is(err, luxmed.ErrAuthentication):
		return "errors.needs_login"
	case errors.Is(err, luxmed.ErrBrowserBusy):
		return "watch.browser_busy"
	case errors.Is(err, luxmed.ErrUnavailable):
		return "errors.luxmed_unavailable"
	default:
		return "errors.generic"
	}
}

func (b *Bot) withAccountRecovery(
	user domain.User,
	key string,
	markup tgbotapi.InlineKeyboardMarkup,
) tgbotapi.InlineKeyboardMarkup {
	var button tgbotapi.InlineKeyboardButton
	switch key {
	case "errors.needs_login":
		button = tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.sign_in"), "account")
	case "errors.no_account":
		button = tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.connect"), "account")
	default:
		return markup
	}
	markup.InlineKeyboard = append([][]tgbotapi.InlineKeyboardButton{{button}}, markup.InlineKeyboard...)
	return markup
}

func (b *Bot) showAccount(ctx context.Context, user domain.User) {
	if b.cfg.Browsers == nil {
		b.setFlow(user.TelegramUserID, &flow{Kind: flowAccountLogin})
		b.send(user, b.catalog.T(user.Locale, "flow.account.login"), cancelKeyboard(b.catalog, user.Locale))
		return
	}
	browser := b.cfg.Browsers[user.TelegramUserID]
	if !user.IsActive() || browser == nil {
		return
	}
	b.clearFlow(user.TelegramUserID)
	if b.cfg.MiniApp != nil {
		b.showMiniApp(ctx, user)
		return
	}
	link, err := browser.ViewerLink(ctx, i18n.Normalize(user.Locale, b.cfg.DefaultLocale))
	if err != nil {
		b.logger.Warn("open browser viewer failed", "err", err)
		b.send(user, b.catalog.T(user.Locale, "errors.luxmed_unavailable"), nil)
		return
	}
	if strings.HasPrefix(link, "http://localhost:") {
		message := tgbotapi.NewMessage(user.TelegramChatID, b.catalog.T(user.Locale, "flow.browser.open")+"\n\n"+link)
		message.DisableWebPagePreview = true
		if _, err := b.api.Send(message); err != nil {
			b.logger.Warn("send browser viewer address failed")
		}
		return
	}
	button := tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.sign_in"), link)
	b.send(user, b.catalog.T(user.Locale, "flow.browser.open"), tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(button)))
}

func (b *Bot) showMiniApp(ctx context.Context, user domain.User) {
	link, err := b.cfg.MiniApp.Link(ctx, user.TelegramUserID)
	if err != nil {
		b.logger.Warn("Mini App unavailable", "err", err)
		b.send(user, b.catalog.T(user.Locale, "errors.luxmed_unavailable"), nil)
		return
	}
	b.sendMiniApp(user, "flow.browser.open", "button.sign_in", link+"&lang="+i18n.Normalize(user.Locale, "pl"))
}

// The pinned Bot API client predates the web_app field.
type miniAppButton struct {
	tgbotapi.InlineKeyboardButton
	WebApp       map[string]string `json:"web_app,omitempty"`
	WidgetAction string            `json:"-"`
}

func newMiniAppButton(label, link string) miniAppButton {
	return miniAppButton{
		InlineKeyboardButton: tgbotapi.InlineKeyboardButton{Text: label},
		WebApp:               map[string]string{"url": link},
	}
}

func (b *Bot) sendMiniApp(user domain.User, messageKey, buttonKey, link string) {
	markup, err := json.Marshal(map[string][][]miniAppButton{
		"inline_keyboard": {{newMiniAppButton(b.catalog.T(user.Locale, buttonKey), link)}},
	})
	if err != nil {
		b.logger.Error("encode Mini App button", "err", err)
		return
	}
	_, err = b.api.MakeRequest("sendMessage", tgbotapi.Params{
		"chat_id":      strconv.FormatInt(user.TelegramChatID, 10),
		"text":         b.catalog.T(user.Locale, messageKey),
		"reply_markup": string(markup),
	})
	if err != nil {
		b.logger.Warn("send Mini App button failed")
	}
}
