package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
)

func (b *Bot) NotifyLuxMedReconnect(ctx context.Context, user domain.User) error {
	if !user.IsActive() || user.TelegramChatID <= 0 || user.TelegramChatID != user.TelegramUserID {
		return errors.New("reconnect recipient has no private chat")
	}
	button := b.actionButton(user, "button.connect_luxmed", "account")
	if b.cfg.MiniApp != nil && b.cfg.Browsers[user.TelegramUserID] != nil {
		button = newMiniAppButton(button.Text, b.cfg.MiniApp.DashboardURL()+"?tab=settings")
	}
	msg := tgbotapi.NewMessage(user.TelegramChatID, b.catalog.T(user.Locale, "notification.search_paused"))
	msg.ReplyMarkup = map[string][][]miniAppButton{"inline_keyboard": {{button}}}
	if ctx.Err() != nil {
		return &scheduler.ReconnectDeliveryRejected{BeforeSend: true}
	}
	_, err := b.api.Request(msg)
	if err == nil {
		return nil
	}
	if rejected, ok := errors.AsType[*tgbotapi.Error](err); ok {
		switch {
		case rejected.Code == http.StatusTooManyRequests || rejected.Code >= http.StatusInternalServerError:
			return &scheduler.ReconnectDeliveryRejected{RetryAfter: time.Duration(rejected.RetryAfter) * time.Second}
		case rejected.Code >= http.StatusBadRequest:
			return errors.New("telegram permanently rejected reconnect warning")
		}
	}
	if connErr, ok := errors.AsType[*net.OpError](err); ok && connErr.Op == "dial" {
		return &scheduler.ReconnectDeliveryRejected{BeforeSend: true}
	}
	return errors.New("telegram reconnect delivery outcome unknown")
}

func (b *Bot) checkAccount(ctx context.Context, user domain.User) error {
	if b.cfg.Browsers != nil {
		browser := b.cfg.Browsers[user.TelegramUserID]
		if browser == nil {
			return luxmed.ErrAuthentication
		}
		return browser.Check(ctx)
	}
	_, err := b.db.LuxMedAccount(ctx, user.ID)
	return err
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
		button = tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.sign_in"), "connect:open")
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
		b.setFlow(user.ID, &flow{Kind: flowAccountLogin})
		b.send(user, b.catalog.T(user.Locale, "flow.account.login"), cancelKeyboard(b.catalog, user.Locale))
		return
	}
	browser := b.cfg.Browsers[user.TelegramUserID]
	if !user.IsActive() || browser == nil {
		return
	}
	if b.cfg.MiniApp != nil {
		b.showMiniApp(ctx, user)
		return
	}
	link, err := browser.ViewerLink(ctx)
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
