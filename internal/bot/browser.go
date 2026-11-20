package bot

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

func (b *Bot) checkAccount(ctx context.Context, user domain.User) error {
	if b.cfg.Browser != nil {
		if !b.cfg.AdminTelegramIDs[user.TelegramUserID] {
			return luxmed.ErrAuthentication
		}
		return b.cfg.Browser.Check(ctx)
	}
	_, err := b.db.LuxMedAccount(ctx, user.ID)
	return err
}

func (b *Bot) showAccount(ctx context.Context, user domain.User) {
	if b.cfg.Browser == nil {
		b.setFlow(user.ID, &flow{Kind: flowAccountLogin})
		b.send(user, b.catalog.T(user.Locale, "flow.account.login"), cancelKeyboard(b.catalog, user.Locale))
		return
	}
	if !user.IsActive() || !b.cfg.AdminTelegramIDs[user.TelegramUserID] {
		return
	}
	b.clearFlow(user.ID)
	if b.cfg.MiniApp != nil {
		b.showMiniApp(ctx, user)
		return
	}
	link, err := b.cfg.Browser.ViewerLink(ctx)
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
	button := tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.open_luxmed"), link)
	b.send(user, b.catalog.T(user.Locale, "flow.browser.open"), tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(button)))
}

func (b *Bot) showMiniApp(ctx context.Context, user domain.User) {
	link, err := b.cfg.MiniApp.Link(ctx)
	if err != nil {
		b.logger.Warn("Mini App unavailable", "err", err)
		b.send(user, b.catalog.T(user.Locale, "errors.luxmed_unavailable"), nil)
		return
	}
	type button struct {
		Text   string            `json:"text"`
		WebApp map[string]string `json:"web_app"`
	}
	markup, err := json.Marshal(map[string][][]button{"inline_keyboard": {{
		{Text: b.catalog.T(user.Locale, "button.open_luxmed"), WebApp: map[string]string{"url": link}},
	}}})
	if err != nil {
		b.logger.Error("encode Mini App button", "err", err)
		return
	}
	// The pinned Bot API client predates the web_app field.
	_, err = b.api.MakeRequest("sendMessage", tgbotapi.Params{
		"chat_id":      strconv.FormatInt(user.TelegramChatID, 10),
		"text":         b.catalog.T(user.Locale, "flow.browser.miniapp"),
		"reply_markup": string(markup),
	})
	if err != nil {
		b.logger.Warn("send Mini App button failed")
	}
}
