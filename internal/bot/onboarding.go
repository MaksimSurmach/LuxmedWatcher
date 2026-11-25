package bot

import (
	"context"
	"encoding/json"
	"strconv"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

func (b *Bot) registerCommands() {
	scope := tgbotapi.NewBotCommandScopeAllPrivateChats()
	for _, language := range append([]string{""}, i18n.SupportedLocales...) {
		commands := []tgbotapi.BotCommand{
			{Command: "start", Description: b.catalog.T(language, "command.start")},
			{Command: "app", Description: b.catalog.T(language, "button.open_app")},
			{Command: "connect", Description: b.catalog.T(language, "button.connect")},
			{Command: "new", Description: b.catalog.T(language, "button.new_watch")},
			{Command: "watches", Description: b.catalog.T(language, "button.my_watches")},
			{Command: "history", Description: b.catalog.T(language, "button.history")},
			{Command: "book", Description: b.catalog.T(language, "button.book_portal")},
			{Command: "settings", Description: b.catalog.T(language, "button.settings")},
			{Command: "language", Description: b.catalog.T(language, "button.language")},
			{Command: "menu", Description: b.catalog.T(language, "button.menu")},
			{Command: "help", Description: b.catalog.T(language, "command.help")},
			{Command: "cancel", Description: b.catalog.T(language, "button.cancel")},
		}
		if _, err := b.api.Request(tgbotapi.NewSetMyCommandsWithScopeAndLanguage(scope, language, commands...)); err != nil {
			b.logger.Warn("register Telegram bot commands failed", "language", language)
		}
	}
}

func (b *Bot) setAppMenu(user domain.User) {
	if b.cfg.MiniApp == nil || !user.IsActive() {
		return
	}
	button, err := json.Marshal(struct {
		Type   string            `json:"type"`
		Text   string            `json:"text"`
		WebApp map[string]string `json:"web_app"`
	}{
		Type:   "web_app",
		Text:   b.catalog.T(user.Locale, "button.open_app"),
		WebApp: map[string]string{"url": b.cfg.MiniApp.DashboardURL()},
	})
	if err != nil {
		b.logger.Warn("encode Telegram chat menu failed")
		return
	}
	_, err = b.api.MakeRequest("setChatMenuButton", tgbotapi.Params{
		"chat_id":     strconv.FormatInt(user.TelegramChatID, 10),
		"menu_button": string(button),
	})
	if err != nil {
		b.logger.Warn("set Telegram chat menu failed")
	}
}

func (b *Bot) showApp(user domain.User, query string) {
	if b.cfg.MiniApp == nil {
		b.showMenu(user)
		return
	}
	b.setAppMenu(user)
	b.sendMiniApp(user, "flow.browser.dashboard", "button.open_app", b.cfg.MiniApp.DashboardURL()+query)
}

func (b *Bot) showStart(user domain.User, invited bool) {
	const title = "Luxmed Watcher"
	if !user.IsActive() {
		b.setFlow(user.ID, &flow{Kind: flowInvite})
		b.sendOnboarding(user, title, b.catalog.T(user.Locale, "flow.invite.enter"), widgetKeyboard(languageKeyboard()))
		return
	}
	b.setAppMenu(user)
	body := b.catalog.T(user.Locale, "widget.start")
	if b.cfg.Browsers != nil {
		body += "\n\n" + b.catalog.T(user.Locale, "widget.start_connect")
	}
	if invited {
		body = b.catalog.T(user.Locale, "flow.invite.accepted") + "\n\n" + body
	}
	connect := b.actionButton(user, "button.connect", "account")
	language := b.actionButton(user, "button.language", "settings:language")
	settings := b.actionButton(user, "button.settings", "settings")
	watches := b.actionButton(user, "button.my_watches", "watches")
	newWatch := b.actionButton(user, "button.new_watch", "new")
	history := b.actionButton(user, "button.history", "history")
	actions := [][]miniAppButton{{connect}, {newWatch, watches}, {history, settings}, {language}}
	if b.cfg.MiniApp != nil {
		app := newMiniAppButton(b.catalog.T(user.Locale, "button.open_app"), b.cfg.MiniApp.DashboardURL())
		app.WidgetAction = "app"
		actions = [][]miniAppButton{{app}, {connect, language}, {newWatch, watches}, {history, settings}}
	}
	b.sendOnboarding(user, title, body, actions)
}

func (b *Bot) showBooking(user domain.User) {
	button := tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.book_portal"), luxmed.ReservationPageURL)
	b.sendOnboarding(user, b.catalog.T(user.Locale, "button.book_portal"), b.catalog.T(user.Locale, "notification.booking_hint"),
		widgetKeyboard(tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(button))))
}

func (b *Bot) showLanguage(user domain.User) {
	b.sendOnboarding(user, b.catalog.T(user.Locale, "flow.language.choose"), "", widgetKeyboard(languageKeyboard()))
}

func (b *Bot) showConnect(ctx context.Context, user domain.User) {
	if b.cfg.Browsers == nil {
		b.showAccount(ctx, user)
		return
	}
	b.clearFlow(user.ID)
	b.sendOnboarding(user, b.catalog.T(user.Locale, "button.connect"), b.catalog.T(user.Locale, "widget.connect"), [][]miniAppButton{
		{b.actionButton(user, "button.sign_in", "connect:open")},
		{b.actionButton(user, "button.menu", "menu")},
	})
}

func (b *Bot) actionButton(user domain.User, key, action string) miniAppButton {
	return miniAppButton{InlineKeyboardButton: tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, key), action)}
}

func widgetKeyboard(keyboard tgbotapi.InlineKeyboardMarkup) [][]miniAppButton {
	rows := make([][]miniAppButton, len(keyboard.InlineKeyboard))
	for i, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			rows[i] = append(rows[i], miniAppButton{InlineKeyboardButton: button})
		}
	}
	return rows
}

func (b *Bot) sendOnboarding(user domain.User, title, body string, actions [][]miniAppButton) {
	text := title
	if body != "" {
		text += "\n\n" + body
	}
	msg := tgbotapi.NewMessage(user.TelegramChatID, text)
	msg.ReplyMarkup = map[string][][]miniAppButton{"inline_keyboard": actions}
	card := widgetCard{Locale: user.Locale, Title: title, Body: body, Actions: actions, Failure: b.catalog.T(user.Locale, "widget.error")}
	if _, err := b.sendWidget(card, text, msg); err != nil {
		b.logger.Warn("send Telegram onboarding message failed")
	}
}
