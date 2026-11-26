package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

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

// SyncAppMenu refreshes the persistent Telegram menu from the saved preferences.
func (b *Bot) SyncAppMenu(ctx context.Context, userID int64) {
	if b.cfg.MiniApp == nil {
		return
	}
	lock, _ := b.appMenuLocks.LoadOrStore(userID, &sync.Mutex{})
	menu := lock.(*sync.Mutex)
	menu.Lock()
	defer menu.Unlock()

	// Serialize writes to the same Telegram menu and read the locale after acquiring its lock.
	user, err := b.db.UserByID(ctx, userID)
	if err != nil {
		b.logger.Warn("load Telegram chat menu preferences failed")
		return
	}
	if !user.IsActive() || user.TelegramChatID <= 0 || user.TelegramChatID != user.TelegramUserID {
		return
	}
	button, err := json.Marshal(struct {
		Type   string            `json:"type"`
		Text   string            `json:"text"`
		WebApp map[string]string `json:"web_app"`
	}{
		Type:   "web_app",
		Text:   b.catalog.T(user.Locale, "button.open_app"),
		WebApp: map[string]string{"url": b.dashboardURL(user, "")},
	})
	if err != nil {
		b.logger.Warn("encode Telegram chat menu failed")
		return
	}
	api := *b.api
	if b.cfg.MenuClient != nil {
		api.Client = b.cfg.MenuClient
	}
	client := &telegramStatusHTTP{HTTPClient: api.Client}
	api.Client = client
	params := tgbotapi.Params{
		"chat_id":     strconv.FormatInt(user.TelegramChatID, 10),
		"menu_button": string(button),
	}
	_, err = api.MakeRequest("setChatMenuButton", params)
	temporary := client.status == 0 || (client.status >= http.StatusInternalServerError && client.status < 600)
	if err != nil && temporary {
		// Reapplying the same button is safe if the first response was lost.
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
			if ctx.Err() != nil {
				return
			}
			_, err = api.MakeRequest("setChatMenuButton", params)
		}
	}
	if err != nil {
		b.logger.Warn("set Telegram chat menu failed")
	}
}

func (b *Bot) dashboardURL(user domain.User, tab string) string {
	query := url.Values{"lang": {i18n.Normalize(user.Locale, b.cfg.DefaultLocale)}}
	if tab != "" {
		query.Set("tab", tab)
	}
	return b.cfg.MiniApp.DashboardURL() + "?" + query.Encode()
}

func (b *Bot) showApp(ctx context.Context, user domain.User, tab string) {
	if b.cfg.MiniApp == nil {
		b.showMenu(user)
		return
	}
	b.SyncAppMenu(ctx, user.ID)
	b.sendMiniApp(user, "flow.browser.dashboard", "button.open_app", b.dashboardURL(user, tab))
}

func (b *Bot) showStart(ctx context.Context, user domain.User, invited bool) {
	const title = "Luxmed Watcher"
	if !user.IsActive() {
		b.setFlow(user.TelegramUserID, &flow{Kind: flowInvite})
		b.sendOnboarding(user, title, b.catalog.T(user.Locale, "flow.invite.enter"), widgetKeyboard(languageKeyboard()))
		return
	}
	b.SyncAppMenu(ctx, user.ID)
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
		app := newMiniAppButton(b.catalog.T(user.Locale, "button.open_app"), b.dashboardURL(user, ""))
		app.WidgetAction = "app"
		actions = [][]miniAppButton{{app}, {connect, language}, {newWatch, watches}, {history, settings}}
	}
	b.sendOnboarding(user, title, body, actions)
}

func (b *Bot) showBooking(user domain.User) {
	button := tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.book_portal"), luxmed.ReservationPageURL)
	b.sendOnboarding(
		user,
		b.catalog.T(user.Locale, "button.book_portal"),
		b.catalog.T(user.Locale, "flow.booking.open"),
		widgetKeyboard(tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(button))),
	)
}

func (b *Bot) showLanguage(user domain.User) {
	b.sendOnboarding(user, b.catalog.T(user.Locale, "flow.language.choose"), "", widgetKeyboard(languageKeyboard()))
}

func (b *Bot) showConnect(ctx context.Context, user domain.User) {
	if b.cfg.Browsers == nil {
		b.showAccount(ctx, user)
		return
	}
	revision, saved, stored, err := b.db.BrowserLoginMode(ctx, user.ID)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
		return
	}
	mode, label := "chrome", "button.chrome"
	if saved {
		mode, label = "switch", "button.chrome_switch"
	}
	shownRevision := strconv.FormatInt(revision, 10)
	action := "connect:" + mode + ":" + shownRevision
	actions := [][]miniAppButton{
		{b.actionButton(user, label, action)},
		{b.actionButton(user, "button.saved_login", "connect:password")},
	}
	if stored {
		actions = append(actions, []miniAppButton{b.actionButton(user, "button.delete_credentials", "connect:forget:"+shownRevision)})
	}
	actions = append(actions, []miniAppButton{b.actionButton(user, "button.menu", "menu")})
	b.sendOnboarding(user, b.catalog.T(user.Locale, "button.connect"), b.catalog.T(user.Locale, "widget.login_method"), actions)
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
