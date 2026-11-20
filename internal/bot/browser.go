package bot

import (
	"context"
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
