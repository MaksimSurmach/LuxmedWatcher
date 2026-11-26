package bot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	randv2 "math/rand/v2"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/procedures"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

type Config struct {
	DefaultLocale    string
	AdminTelegramIDs map[int64]bool
	MinimumInterval  time.Duration
	DefaultInterval  time.Duration
	PollTimeout      int
	Browsers         map[int64]*luxmed.Browser
	MiniApp          *luxmed.MiniApp
	MenuClient       tgbotapi.HTTPClient
}

type Bot struct {
	api           *tgbotapi.BotAPI
	db            *storage.DB
	catalog       *i18n.Catalog
	cryptor       *security.Cryptor
	cfg           Config
	runner        *scheduler.Runner
	manualChecks  chan struct{}
	manualWorkers sync.WaitGroup
	appMenuLocks  sync.Map // User ID to *sync.Mutex.
	logger        *slog.Logger
	newClient     func(bool) luxmed.Client
	flowState     map[int64]*flow
}

type flowKind string

const (
	flowNone          flowKind = ""
	flowInvite        flowKind = "invite"
	flowAccountLogin  flowKind = "account_login"
	flowAccountPass   flowKind = "account_pass"
	flowWatchCity     flowKind = "watch_city"
	flowCitySearch    flowKind = "city_search"
	flowWatchService  flowKind = "watch_service"
	flowWatchSearch   flowKind = "watch_search"
	flowWatchDoctor   flowKind = "watch_doctor"
	flowDoctorSearch  flowKind = "doctor_search"
	flowWatchFacility flowKind = "watch_facility"
	flowWatchTime     flowKind = "watch_time"
	flowWatchDate     flowKind = "watch_date"
	flowWatchDays     flowKind = "watch_days"
	flowWatchInterval flowKind = "watch_interval"
	flowWatchConfirm  flowKind = "watch_confirm"
)

type flow struct {
	Kind                 flowKind
	Login                string
	CitySettings         bool
	ProcedureSearchQuery string
	CatalogLoaded        bool
	Doctors              []domain.Doctor
	Confirmation         string
	Watch                domain.Watch
}

func New(api *tgbotapi.BotAPI, db *storage.DB, catalog *i18n.Catalog, cryptor *security.Cryptor, cfg Config, logger *slog.Logger) *Bot {
	return &Bot{api: api, db: db, catalog: catalog, cryptor: cryptor, cfg: cfg, logger: logger, flowState: make(map[int64]*flow), manualChecks: make(chan struct{}, 3),
		newClient: func(raw bool) luxmed.Client { return luxmed.NewHTTPClient(raw) }}
}

func (b *Bot) SetRunner(runner *scheduler.Runner) {
	b.runner = runner
}

func (b *Bot) Run(ctx context.Context) error {
	defer b.manualWorkers.Wait()
	b.registerCommands()
	config := tgbotapi.NewUpdate(0)
	config.Timeout = b.cfg.PollTimeout
	config.AllowedUpdates = []string{"message", "callback_query", "widget_query"}
	var delay time.Duration
	for ctx.Err() == nil {
		updates, err := b.getUpdates(config)
		if err != nil {
			b.logger.Warn("Telegram polling failed", "retrying", true)
			delay = min(30*time.Second, max(time.Second, 2*delay))
			if apiErr, ok := errors.AsType[*tgbotapi.Error](err); ok && apiErr.RetryAfter > 0 {
				delay = max(delay, time.Duration(apiErr.RetryAfter)*time.Second)
			}
			timer := time.NewTimer(delay + time.Duration(randv2.IntN(500))*time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		delay = 0
		for _, update := range updates {
			if ctx.Err() != nil {
				return nil
			}
			if update.UpdateID < config.Offset {
				continue
			}
			if update.WidgetQuery != nil {
				b.handleWidgetQuery(ctx, update.WidgetQuery)
			} else {
				b.handleUpdate(ctx, update.Update)
			}
			config.Offset = update.UpdateID + 1
		}
	}
	return nil
}

func (b *Bot) NotifyAppointments(ctx context.Context, user domain.User, watch domain.Watch, histories []domain.AppointmentHistory, total int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !user.IsActive() || user.ID != watch.UserID || user.TelegramChatID <= 0 || user.TelegramChatID != user.TelegramUserID {
		return 0, fmt.Errorf("notification recipient has no private chat")
	}
	if len(histories) == 0 {
		return 0, nil
	}
	text, shown := b.appointmentNotification(user.Locale, watch, histories, total, maxNotificationSlots)
	msg := tgbotapi.NewMessage(user.TelegramChatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.DisableWebPagePreview = true
	booking := miniAppButton{InlineKeyboardButton: tgbotapi.NewInlineKeyboardButtonURL(
		b.catalog.T(user.Locale, "button.book_portal"), portalURL(histories[0].BookingURL),
	)}
	details := b.actionButton(user, "button.show_watch", fmt.Sprintf("watch:show:%d", watch.ID))
	keyboard := [][]miniAppButton{{booking}, {details}}
	if b.cfg.MiniApp != nil && b.cfg.Browsers[user.TelegramUserID] != nil {
		results := newMiniAppButton(b.catalog.T(user.Locale, "button.slot_results"), b.dashboardURL(user, "history"))
		results.WidgetAction = "app:history"
		keyboard[1] = []miniAppButton{results}
		keyboard = append(keyboard, []miniAppButton{details})
	}
	if max(total, len(histories)) > shown {
		keyboard[1] = append(keyboard[1], b.actionButton(user, "button.all_options", fmt.Sprintf("options:%d:0", watch.ID)))
	}
	keyboard = append(keyboard, []miniAppButton{
		b.actionButton(user, "button.pause", fmt.Sprintf("watch:pause:%d", watch.ID)),
		b.actionButton(user, "button.check_again", fmt.Sprintf("watch:check:%d", watch.ID)),
	})
	msg.ReplyMarkup = map[string][][]miniAppButton{"inline_keyboard": keyboard}
	if max(total, len(histories)) > 1 {
		// The widget card has room for one slot; grouped lists go in the text message.
		res, err := b.api.Request(msg)
		var sent struct {
			MessageID int `json:"message_id"`
		}
		if err != nil || json.Unmarshal(res.Result, &sent) != nil || sent.MessageID <= 0 {
			return 0, fmt.Errorf("Telegram notification delivery failed")
		}
		return sent.MessageID, nil
	}
	title := b.catalog.T(user.Locale, "notification.title", 1)
	card := widgetCard{
		Locale:      user.Locale,
		Title:       title,
		Body:        truncateMessage(procedures.WatchTitle(watch), 150),
		Slot:        newWidgetSlot(user.Locale, histories[0]),
		Actions:     keyboard[:2],
		BookingHint: b.catalog.T(user.Locale, "notification.booking_hint"),
		Failure:     b.catalog.T(user.Locale, "widget.error"),
	}
	caption, _ := b.appointmentNotification(user.Locale, watch, histories, total, 2)
	messageID, err := b.sendWidget(card, caption, msg)
	if err != nil {
		return 0, fmt.Errorf("Telegram notification delivery failed")
	}
	return messageID, nil
}

func portalURL(raw string) string {
	const portal = luxmed.ReservationPageURL
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "portalpacjenta.luxmed.pl" || u.User != nil || (u.Path != "/PatientPortal/NewPortal/Page/Reservation" && u.Path != "/PatientPortal/NewPortal/Page/Reservation/Search") || u.RawQuery != "" || u.Fragment != "" {
		return portal
	}
	return u.String()
}

func privateChat(chat *tgbotapi.Chat, from *tgbotapi.User) bool {
	return chat != nil && from != nil && !from.IsBot && from.ID > 0 && chat.IsPrivate() && chat.ID == from.ID
}

func truncateMessage(text string, limit int) string {
	for i, r := range text {
		if r > 0xFFFF {
			limit--
		}
		limit--
		if limit < 0 {
			return text[:i]
		}
	}
	return text
}

func (b *Bot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		b.handleCallback(ctx, update.CallbackQuery)
		return
	}
	if update.Message == nil || !privateChat(update.Message.Chat, update.Message.From) ||
		(b.cfg.Browsers != nil && b.cfg.Browsers[update.Message.From.ID] == nil) {
		return
	}
	user, err := b.upsertUser(ctx, update.Message)
	if err != nil {
		b.logger.Warn("upsert user failed", "err", err)
		return
	}
	if user.Status == domain.UserStatusBlocked {
		b.clearFlow(user.ID)
		b.send(user, b.catalog.T(user.Locale, "errors.blocked"), nil)
		return
	}
	state := b.getFlow(user.ID)
	if state != nil && state.Kind == flowAccountPass && user.IsActive() && update.Message.Command() != "cancel" {
		if _, err := b.api.Request(tgbotapi.NewDeleteMessage(update.Message.Chat.ID, update.Message.MessageID)); err != nil {
			b.logger.Warn("could not delete Telegram credential message")
		}
		b.handleText(ctx, user, update.Message.Text)
		return
	}
	if update.Message.IsCommand() {
		b.handleCommand(ctx, user, update.Message)
		return
	}
	b.handleText(ctx, user, strings.TrimSpace(update.Message.Text))
}

func (b *Bot) upsertUser(ctx context.Context, msg *tgbotapi.Message) (domain.User, error) {
	locale := i18n.Normalize(msg.From.LanguageCode, b.cfg.DefaultLocale)
	role := domain.UserRoleUser
	status := domain.UserStatusPendingInvite
	if b.cfg.AdminTelegramIDs[msg.From.ID] {
		role = domain.UserRoleAdmin
		status = domain.UserStatusActive
	}
	displayName := strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
	user, err := b.db.UpsertTelegramUser(ctx, domain.User{
		TelegramUserID: msg.From.ID,
		TelegramChatID: msg.Chat.ID,
		Username:       msg.From.UserName,
		DisplayName:    displayName,
		Locale:         locale,
		Status:         status,
		Role:           role,
	})
	if err != nil {
		return domain.User{}, err
	}
	if b.cfg.AdminTelegramIDs[msg.From.ID] && user.Status != domain.UserStatusBlocked && (!user.IsActive() || !user.IsAdmin()) {
		if err := b.db.SetUserActive(ctx, user.ID, domain.UserRoleAdmin); err != nil {
			return domain.User{}, err
		}
		user.Status = domain.UserStatusActive
		user.Role = domain.UserRoleAdmin
	}
	return user, nil
}

func (b *Bot) handleCommand(ctx context.Context, user domain.User, msg *tgbotapi.Message) {
	command := msg.Command()
	if !user.IsActive() && command != "start" && command != "language" && command != "menu" && command != "cancel" {
		b.send(user, b.catalog.T(user.Locale, "errors.invite_required"), languageKeyboard())
		return
	}
	switch command {
	case "start":
		arg := strings.TrimSpace(msg.CommandArguments())
		if arg != "" && !user.IsActive() {
			b.tryRedeemInvite(ctx, user, arg)
			return
		}
		b.showStart(ctx, user, false)
	case "menu":
		b.clearFlow(user.ID)
		b.showStart(ctx, user, false)
	case "new":
		b.startWatchFlow(ctx, user)
	case "app":
		b.showApp(ctx, user, "")
	case "connect":
		b.showConnect(ctx, user)
	case "watches":
		b.showWatches(ctx, user)
	case "history":
		b.showHistory(ctx, user)
	case "book":
		b.showBooking(user)
	case "settings":
		b.send(user, b.catalog.T(user.Locale, "settings.title"), settingsKeyboard(b.catalog, user.Locale))
	case "language":
		b.showLanguage(user)
	case "help":
		b.send(user, b.catalog.T(user.Locale, "help.text"), mainKeyboard(b.catalog, user.Locale))
	case "cancel":
		b.clearFlow(user.ID)
		b.showMenu(user)
	case "logout_luxmed":
		b.clearFlow(user.ID)
		if err := b.db.DeleteLuxMedAccount(ctx, user.ID); err != nil {
			b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
			return
		}
		key := "flow.account.removed"
		if b.cfg.Browsers != nil {
			key = "flow.browser.logout"
		}
		b.send(user, b.catalog.T(user.Locale, key), mainKeyboard(b.catalog, user.Locale))
	case "invite":
		b.createInvite(ctx, user, msg.CommandArguments())
	default:
		b.showMenu(user)
	}
}

func (b *Bot) handleText(ctx context.Context, user domain.User, text string) {
	if !user.IsActive() {
		b.tryRedeemInvite(ctx, user, text)
		return
	}
	state := b.getFlow(user.ID)
	if state == nil || state.Kind == flowNone {
		b.showMenu(user)
		return
	}
	switch state.Kind {
	case flowAccountLogin:
		if text == "" || len(text) > 254 {
			b.send(user, b.catalog.T(user.Locale, "flow.account.login"), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		state.Login = text
		state.Kind = flowAccountPass
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.account.password"), nil)
	case flowAccountPass:
		if text == "" || len(text) > 1024 {
			b.send(user, b.catalog.T(user.Locale, "flow.account.password"), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		if err := b.newClient(false).Authenticate(ctx, luxmed.Credentials{Login: state.Login, Password: text}); err != nil {
			key := "errors.auth_failed"
			if errors.Is(err, luxmed.ErrUnavailable) {
				key = "errors.luxmed_unavailable"
			}
			b.send(user, b.catalog.T(user.Locale, key), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		encrypted, err := b.cryptor.Encrypt(text)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
			return
		}
		now := time.Now().UTC()
		save := b.db.SaveVerifiedLuxMedAccount
		if b.runner != nil {
			save = b.runner.SaveVerifiedLuxMedAccount
		}
		err = save(ctx, domain.LuxMedAccount{
			UserID:            user.ID,
			Login:             state.Login,
			EncryptedPassword: encrypted,
			LastLoginAt:       &now,
			Status:            domain.AccountStatusActive,
		})
		b.clearFlow(user.ID)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
			return
		}
		b.send(user, b.catalog.T(user.Locale, "flow.account.saved"), mainKeyboard(b.catalog, user.Locale))
	case flowWatchCity:
		b.showCityPicker(ctx, user, state.CitySettings, 0)
	case flowCitySearch:
		b.showCitySearchResults(ctx, user, state, text)
	case flowWatchService:
		b.handleProcedureText(ctx, user, state, text)
	case flowWatchSearch:
		b.handleProcedureText(ctx, user, state, text)
	case flowDoctorSearch:
		b.showDoctorResults(user, state, text)
	case flowWatchDate:
		b.handleWatchDate(user, state, text)
	case flowWatchDays:
		b.selectWatchDays(user, state, text)
	case flowWatchInterval:
		minutes, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || minutes <= 0 || minutes > 1440 {
			b.showIntervalStep(user, state)
			return
		}
		b.previewWatch(user, state, time.Duration(minutes)*time.Minute)
	case flowWatchDoctor:
		return
	case flowWatchFacility:
		b.handleFacilityText(ctx, user, state, text)
	case flowWatchTime:
		b.handleTimeText(user, state, text)
	}
}

func (b *Bot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || !privateChat(cb.Message.Chat, cb.From) ||
		(b.cfg.Browsers != nil && b.cfg.Browsers[cb.From.ID] == nil) {
		return
	}
	_, _ = b.api.Request(tgbotapi.NewCallback(cb.ID, ""))
	data := cb.Data
	if strings.HasPrefix(data, "options:") && strings.Count(data, ":") == 2 {
		// Only the opening button is on the notification itself. Page buttons carry its message ID.
		data += ":" + strconv.Itoa(cb.Message.MessageID)
	}
	b.handleAction(ctx, cb.From.ID, data)
}

func (b *Bot) handleAction(ctx context.Context, telegramID int64, data string) {
	user, err := b.db.UserByTelegramID(ctx, telegramID)
	if err != nil {
		return
	}
	if user.Status == domain.UserStatusBlocked {
		b.clearFlow(user.ID)
		b.send(user, b.catalog.T(user.Locale, "errors.blocked"), nil)
		return
	}
	if !user.IsActive() && !strings.HasPrefix(data, "lang:") && data != "settings:language" && data != "cancel" {
		b.send(user, b.catalog.T(user.Locale, "errors.invite_required"), languageKeyboard())
		return
	}
	if !allowsFlowCallback(b.getFlow(user.ID), data) {
		return
	}
	switch {
	case strings.HasPrefix(data, "lang:"):
		locale := strings.TrimPrefix(data, "lang:")
		if !slices.Contains(i18n.SupportedLocales, locale) {
			return
		}
		if err := b.db.SetUserLocale(ctx, user.ID, locale); err != nil {
			b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
			return
		}
		user.Locale = locale
		if !user.IsActive() {
			b.setFlow(user.ID, &flow{Kind: flowInvite})
			b.send(user, b.catalog.T(user.Locale, "flow.invite.enter"), nil)
			return
		}
		b.SyncAppMenu(ctx, user.ID)
		b.send(user, b.catalog.T(user.Locale, "flow.language.saved"), mainKeyboard(b.catalog, user.Locale))
	case data == "menu":
		b.clearFlow(user.ID)
		b.showStart(ctx, user, false)
	case data == "account":
		b.showConnect(ctx, user)
	case data == "connect:open":
		b.showAccount(ctx, user)
	case data == "app":
		b.showApp(ctx, user, "")
	case data == "app:history":
		b.showApp(ctx, user, "history")
	case data == "new":
		b.startWatchFlow(ctx, user)
	case data == "watches":
		b.showWatches(ctx, user)
	case data == "history":
		b.showHistory(ctx, user)
	case data == "settings":
		b.send(user, b.catalog.T(user.Locale, "settings.title"), settingsKeyboard(b.catalog, user.Locale))
	case data == "settings:language":
		b.showLanguage(user)
	case data == "settings:city":
		b.setFlow(user.ID, &flow{Kind: flowWatchCity, CitySettings: true})
		b.showCityPicker(ctx, user, true, 0)
	case data == "cancel":
		b.clearFlow(user.ID)
		b.showMenu(user)
	case data == "city:search" || data == "prefcity:search":
		state := b.getFlow(user.ID)
		state.Kind = flowCitySearch
		b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.city_search"), citySearchKeyboard(b.catalog, user.Locale))

	case strings.HasPrefix(data, "city:page:"):
		page, _ := strconv.Atoi(strings.TrimPrefix(data, "city:page:"))
		b.showCityPicker(ctx, user, false, page)
	case strings.HasPrefix(data, "prefcity:page:"):
		page, _ := strconv.Atoi(strings.TrimPrefix(data, "prefcity:page:"))
		b.showCityPicker(ctx, user, true, page)
	case strings.HasPrefix(data, "prefcity:"):
		b.handlePreferredCity(ctx, user, data)
	case strings.HasPrefix(data, "city:"):
		b.handleWatchCity(ctx, user, data)
	case data == "proc:search":
		state := b.getFlow(user.ID)
		state.Kind = flowWatchSearch
		b.setFlow(user.ID, state)
		b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.service_search"), procedureSearchKeyboard(b.catalog, user.Locale))
	case data == "proc:popular":
		state := b.getFlow(user.ID)
		if state != nil {
			b.showPopularProcedures(ctx, user, state)
		}
	case data == "proc:all":
		state := b.getFlow(user.ID)
		if state != nil {
			b.showProcedureLetters(ctx, user, state)
		}
	case strings.HasPrefix(data, "procletter:"):
		state := b.getFlow(user.ID)
		if state != nil {
			b.handleProcedureLetterPage(ctx, user, state, data)
		}

	case strings.HasPrefix(data, "procsearchpage:"):
		state := b.getFlow(user.ID)
		if state != nil {
			b.handleProcedureSearchPage(ctx, user, state, data)
		}
	case strings.HasPrefix(data, "proc:"):
		b.handleProcedure(ctx, user, data)

	case data == "fac:any":
		state := b.getFlow(user.ID)
		if state != nil {
			state.Watch.FacilityMode = domain.FacilityModeAll
			state.Watch.FacilityIDs = nil
			state.Watch.FacilityNames = nil
			b.setFlow(user.ID, state)
			b.showDateStep(user, state)
		}

	case data == "fac:all":
		state := b.getFlow(user.ID)
		if state != nil {
			b.showFacilityPage(ctx, user, state, "all", 0)
		}

	case data == "fac:favs":
		state := b.getFlow(user.ID)
		if state != nil {
			b.showFacilityPage(ctx, user, state, "favs", 0)
		}

	case strings.HasPrefix(data, "facpage:"):
		state := b.getFlow(user.ID)
		if state != nil {
			b.handleFacilityPage(ctx, user, state, data)
		}

	case strings.HasPrefix(data, "facsel:"):
		state := b.getFlow(user.ID)
		if state != nil {
			b.handleFacilityToggle(ctx, user, state, data)
		}

	case data == "facclear":
		state := b.getFlow(user.ID)
		if state != nil {
			state.Watch.FacilityIDs = nil
			state.Watch.FacilityNames = nil
			state.Watch.FacilityMode = ""
			b.setFlow(user.ID, state)
			b.showFacilityPage(ctx, user, state, "all", 0)
		}

	case data == "facdone":
		state := b.getFlow(user.ID)
		if state != nil {
			b.finishSelectedFacilities(ctx, user, state)
		}
	case strings.HasPrefix(data, "time:"):
		b.handleTimeCallback(user, b.getFlow(user.ID), data)
	case strings.HasPrefix(data, "date:"):
		b.handleWatchDate(user, b.getFlow(user.ID), strings.TrimPrefix(data, "date:"))
	case strings.HasPrefix(data, "days:"):
		b.selectWatchDays(user, b.getFlow(user.ID), strings.TrimPrefix(data, "days:"))
	case strings.HasPrefix(data, "interval:"):
		value := strings.TrimPrefix(data, "interval:")
		interval := max(b.cfg.DefaultInterval, b.cfg.MinimumInterval, time.Minute)
		if value != "default" {
			minutes, err := strconv.Atoi(value)
			if err != nil || minutes <= 0 || minutes > 1440 {
				return
			}
			interval = time.Duration(minutes) * time.Minute
		}
		b.previewWatch(user, b.getFlow(user.ID), interval)
	case strings.HasPrefix(data, "confirm:"):
		b.finishWatch(ctx, user, strings.TrimPrefix(data, "confirm:"))
	case strings.HasPrefix(data, "fac:"):
		b.handleFacility(ctx, user, data)
	case strings.HasPrefix(data, "doctor:"):
		b.handleDoctorChoice(ctx, user, b.getFlow(user.ID), strings.TrimPrefix(data, "doctor:"))
	case strings.HasPrefix(data, "history:"):
		b.handleHistoryPage(ctx, user, data)
	case strings.HasPrefix(data, "options:"):
		b.handleNotificationOptions(ctx, user, strings.TrimPrefix(data, "options:"))
	case strings.HasPrefix(data, "hide:"):
		b.hideHistory(ctx, user, data)
	case strings.HasPrefix(data, "back:"):
		b.backWatch(ctx, user, strings.TrimPrefix(data, "back:"))

	case strings.HasPrefix(data, "watch:"):
		b.handleWatchAction(ctx, user, data)
	}
}

func (b *Bot) startWatchFlow(ctx context.Context, user domain.User) {
	if err := b.checkAccount(ctx, user); err != nil {
		key := accountCheckMessage(err)
		markup := b.withAccountRecovery(user, key, mainKeyboard(b.catalog, user.Locale))
		b.send(user, b.catalog.T(user.Locale, key), markup)
		return
	}
	state := &flow{Kind: flowWatchCity, Watch: domain.Watch{UserID: user.ID, NextDays: 14, DoctorMode: domain.DoctorModeAny, FacilityMode: domain.FacilityModeAll}}
	b.setFlow(user.ID, state)
	if user.PreferredCityID != nil {
		state.Watch.CityID = *user.PreferredCityID
		state.Watch.CityName = user.PreferredCityName
		state.Kind = flowWatchService
		b.setFlow(user.ID, state)
		b.showProcedureMenu(user, state)
		return
	}
	b.showCityPicker(ctx, user, false, 0)
}

func (b *Bot) showCityPicker(ctx context.Context, user domain.User, settings bool, page int) {
	const pageSize = 12
	page = min(max(page, 0), 10000)
	if _, err := b.ensureCities(ctx, user); err != nil {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	cities, err := b.db.CitiesPage(ctx, pageSize+1, page*pageSize)
	if err != nil {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	hasNext := len(cities) > pageSize
	if hasNext {
		cities = cities[:pageSize]
	}
	prefix := "city:"
	if settings {
		prefix = "prefcity:"
	}
	b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.city"), cityKeyboard(cities, prefix, page, page > 0, hasNext, b.catalog, user.Locale))
}

func (b *Bot) showTimeStep(user domain.User, state *flow) {
	state.Kind = flowWatchTime
	b.setFlow(user.ID, state)

	b.sendWatchStep(
		user,
		b.catalog.T(user.Locale, "flow.watch.hours"),
		timeWindowKeyboard(b.catalog, user.Locale),
	)
}

func (b *Bot) handleTimeCallback(user domain.User, state *flow, data string) {
	value := strings.TrimPrefix(data, "time:")

	switch value {
	case "any":
		state.Watch.TimeWindows = nil
	case "morning":
		state.Watch.TimeWindows = []domain.TimeWindow{{From: "08:00", To: "12:00"}}
	case "day":
		state.Watch.TimeWindows = []domain.TimeWindow{{From: "12:00", To: "16:00"}}
	case "evening":
		state.Watch.TimeWindows = []domain.TimeWindow{{From: "16:00", To: "20:00"}}
	case "workday":
		state.Watch.TimeWindows = []domain.TimeWindow{{From: "09:00", To: "18:00"}}
	default:
		b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.hours"), timeWindowKeyboard(b.catalog, user.Locale))
		return
	}

	b.showDaysStep(user, state)
}

func (b *Bot) handleTimeText(user domain.User, state *flow, text string) {
	text = strings.TrimSpace(strings.ToLower(text))

	if text == "" || text == "any" || text == "все" || text == "любое" {
		state.Watch.TimeWindows = nil
		b.showDaysStep(user, state)
		return
	}

	window, ok := parseTimeWindow(text)
	if !ok {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.hours"), timeWindowKeyboard(b.catalog, user.Locale))
		return
	}

	state.Watch.TimeWindows = []domain.TimeWindow{window}
	b.showDaysStep(user, state)
}

func parseTimeWindow(text string) (domain.TimeWindow, bool) {
	text = strings.ReplaceAll(text, " ", "")
	text = strings.ReplaceAll(text, "–", "-")
	text = strings.ReplaceAll(text, "—", "-")

	parts := strings.Split(text, "-")
	if len(parts) != 2 {
		return domain.TimeWindow{}, false
	}

	from := normalizeTime(parts[0])
	to := normalizeTime(parts[1])

	fromTime, errFrom := time.Parse("15:04", from)
	toTime, errTo := time.Parse("15:04", to)

	if errFrom != nil || errTo != nil || !fromTime.Before(toTime) {
		return domain.TimeWindow{}, false
	}

	return domain.TimeWindow{
		From: from,
		To:   to,
	}, true
}

func normalizeTime(value string) string {
	value = strings.TrimSpace(value)

	if len(value) == 1 {
		return "0" + value + ":00"
	}

	if len(value) == 2 {
		return value + ":00"
	}

	if len(value) == 4 && strings.Contains(value, ":") {
		return "0" + value
	}

	return value
}

func (b *Bot) showCitySearchResults(ctx context.Context, user domain.User, state *flow, query string) {
	if _, err := b.ensureCities(ctx, user); err != nil {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	cities, err := b.db.SearchCities(ctx, query, 12)
	if err != nil || len(cities) == 0 {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.city_not_found"), citySearchKeyboard(b.catalog, user.Locale))
		return
	}
	prefix := "city:"
	if state.CitySettings {
		prefix = "prefcity:"
	}
	b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.city_results"), cityKeyboard(cities, prefix, 0, false, false, b.catalog, user.Locale))
}

func (b *Bot) handlePreferredCity(ctx context.Context, user domain.User, data string) {
	id, err := strconv.Atoi(strings.TrimPrefix(data, "prefcity:"))
	if err != nil {
		return
	}
	city, err := b.db.City(ctx, id)
	if err != nil {
		return
	}
	if err := b.db.SetUserPreferredCity(ctx, user.ID, city); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
		return
	}
	b.send(user, b.catalog.T(user.Locale, "settings.city_saved", city.Name), mainKeyboard(b.catalog, user.Locale))
}

func (b *Bot) handleWatchCity(ctx context.Context, user domain.User, data string) {
	state := b.getFlow(user.ID)
	if state == nil {
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(data, "city:"))
	if err != nil {
		return
	}
	city, err := b.db.City(ctx, id)
	if err != nil {
		return
	}
	if state.Watch.CityID != city.ID {
		state.CatalogLoaded = false
	}
	state.Watch.CityID = city.ID
	state.Watch.CityName = city.Name
	state.Kind = flowWatchService
	b.setFlow(user.ID, state)
	b.showProcedureMenu(user, state)
}

func (b *Bot) showProcedureMenu(user domain.User, state *flow) {
	state.Kind = flowWatchService
	state.ProcedureSearchQuery = ""
	b.setFlow(user.ID, state)
	b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.service"), procedureMenuKeyboard(nil, b.catalog, user.Locale))
}

func (b *Bot) showPopularProcedures(ctx context.Context, user domain.User, state *flow) {
	state.Kind = flowWatchService
	state.ProcedureSearchQuery = ""
	b.setFlow(user.ID, state)

	client, err := b.authenticatedLuxMedClient(ctx, user)
	if err != nil {
		b.logger.Warn(
			"authenticate luxmed client for popular procedures failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"err", err,
		)
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), procedureMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}

	popular, err := client.GetPopularProcedures(ctx)
	if err != nil {
		b.logger.Warn(
			"load popular procedures failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"err", err,
		)
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), procedureMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}

	if len(popular) == 0 {
		b.logger.Warn(
			"popular procedures empty",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
		)
		b.send(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}

	if err := b.db.UpsertProcedures(ctx, domain.City{ID: state.Watch.CityID, Name: state.Watch.CityName}, popular); err != nil {
		b.logger.Warn(
			"upsert popular procedures failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"popular_count", len(popular),
			"err", err,
		)
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), procedureMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}

	b.logger.Info(
		"show popular procedures",
		"user_id", user.ID,
		"city_id", state.Watch.CityID,
		"city_name", state.Watch.CityName,
		"popular_count", len(popular),
	)

	title := b.catalog.T(user.Locale, "button.popular_procedures")
	b.sendProcedureOptions(user, title, popular, procedureResultsKeyboard(popular, "", "", b.catalog, user.Locale))
}

func (b *Bot) showProcedureSearchResults(ctx context.Context, user domain.User, state *flow, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.service_search"), procedureSearchKeyboard(b.catalog, user.Locale))
		return
	}

	state.Kind = flowWatchSearch
	state.ProcedureSearchQuery = query
	b.setFlow(user.ID, state)

	b.showProcedureSearchResultsPage(ctx, user, state, query, 0)
}

func (b *Bot) handleProcedure(ctx context.Context, user domain.User, data string) {
	state := b.getFlow(user.ID)
	if state == nil {
		return
	}

	id, err := strconv.Atoi(strings.TrimPrefix(data, "proc:"))
	if err != nil {
		return
	}

	proc, err := b.db.Procedure(ctx, state.Watch.CityID, id)
	if err != nil {
		b.logger.Warn(
			"procedure callback not found",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"procedure_id", id,
			"err", err,
		)
		return
	}

	b.selectProcedure(user, state, proc)
}

func (b *Bot) showFacilityMenu(ctx context.Context, user domain.User, state *flow) {
	city := domain.City{ID: state.Watch.CityID, Name: state.Watch.CityName}
	if err := b.ensureFacilities(ctx, user, city, state.Watch.ServiceID); err != nil {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}

	state.Kind = flowWatchFacility
	b.setFlow(user.ID, state)

	favorites, err := b.db.FavoriteFacilities(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, 3)
	if err != nil {
		b.logger.Warn(
			"load favorite facilities failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"procedure_id", state.Watch.ServiceID,
			"err", err,
		)
	}

	if len(favorites) > 0 {
		title := b.catalog.T(user.Locale, "flow.watch.facility_favorites")
		b.sendFacilityOptions(
			user,
			title,
			favorites,
			facilityResultsKeyboard(favorites, "favs", 0, "", "", state.Watch.FacilityIDs, b.catalog, user.Locale),
		)
		return
	}

	b.sendWatchStep(
		user,
		b.catalog.T(user.Locale, "flow.watch.facility"),
		facilityMenuKeyboard(nil, b.catalog, user.Locale),
	)
}

func (b *Bot) handleFacility(ctx context.Context, user domain.User, data string) {
	state := b.getFlow(user.ID)
	if state == nil {
		return
	}

	id, err := strconv.Atoi(strings.TrimPrefix(data, "fac:"))
	if err != nil {
		return
	}

	facility, err := b.findFacilityByID(ctx, state, id)
	if err != nil {
		b.logger.Warn(
			"facility callback not found",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"procedure_id", state.Watch.ServiceID,
			"facility_id", id,
			"err", err,
		)
		return
	}

	state.Watch.FacilityMode = domain.FacilityModeSelected
	state.Watch.FacilityIDs = []int{facility.ID}
	state.Watch.FacilityNames = []string{facility.Name}

	if err := b.db.SaveFavoriteFacility(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, facility); err != nil {
		b.logger.Warn("save favorite facility failed")
	}

	b.setFlow(user.ID, state)
	b.showDateStep(user, state)
}

func (b *Bot) handleFacilityText(ctx context.Context, user domain.User, state *flow, text string) {
	text = strings.TrimSpace(text)

	if strings.EqualFold(text, "all") || strings.EqualFold(text, "все") {
		state.Watch.FacilityMode = domain.FacilityModeAll
		state.Watch.FacilityIDs = nil
		state.Watch.FacilityNames = nil
		b.setFlow(user.ID, state)
		b.showDateStep(user, state)
		return
	}

	id, _, ok := parseIDName(text)
	if !ok {
		b.showFacilityPage(ctx, user, state, "all", 0)
		return
	}

	facility, err := b.findFacilityByID(ctx, state, id)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "flow.watch.facility"), facilityMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}

	state.Watch.FacilityMode = domain.FacilityModeSelected
	state.Watch.FacilityIDs = []int{facility.ID}
	state.Watch.FacilityNames = []string{facility.Name}

	if err := b.db.SaveFavoriteFacility(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, facility); err != nil {
		b.logger.Warn("save favorite facility failed")
	}

	b.setFlow(user.ID, state)
	b.showDateStep(user, state)
}

func (b *Bot) handleFacilityPage(ctx context.Context, user domain.User, state *flow, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return
	}

	mode := parts[1]
	page, err := strconv.Atoi(parts[2])
	if err != nil || page < 0 {
		page = 0
	}

	b.showFacilityPage(ctx, user, state, mode, page)
}

func (b *Bot) showFacilityPage(ctx context.Context, user domain.User, state *flow, mode string, page int) {
	const pageSize = 10

	page = min(max(page, 0), 10000)

	offset := page * pageSize

	var (
		facilities []domain.Facility
		err        error
		title      string
	)

	switch mode {
	case "favs":
		facilities, err = b.db.FavoriteFacilitiesPage(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, pageSize+1, offset)
		title = b.catalog.T(user.Locale, "flow.watch.facility_favorites")
	default:
		mode = "all"
		facilities, err = b.db.FacilitiesPage(ctx, state.Watch.CityID, state.Watch.ServiceID, pageSize+1, offset)
		title = b.catalog.T(user.Locale, "flow.watch.facility_all")
	}

	if err != nil || len(facilities) == 0 {
		if mode == "favs" {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.facility_no_favorites"), facilityMenuKeyboard(nil, b.catalog, user.Locale))
			return
		}

		b.send(user, b.catalog.T(user.Locale, "flow.watch.facility_empty"), facilityMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}

	hasNext := len(facilities) > pageSize
	if hasNext {
		facilities = facilities[:pageSize]
	}

	previousCallback := ""
	if page > 0 {
		previousCallback = fmt.Sprintf("facpage:%s:%d", mode, page-1)
	}

	nextCallback := ""
	if hasNext {
		nextCallback = fmt.Sprintf("facpage:%s:%d", mode, page+1)
	}

	text := title + "\n" + b.catalog.T(user.Locale, "flow.watch.facility_selection", len(state.Watch.FacilityIDs))
	b.sendFacilityOptions(
		user,
		text,
		facilities,
		facilityResultsKeyboard(facilities, mode, page, previousCallback, nextCallback, state.Watch.FacilityIDs, b.catalog, user.Locale),
	)
}

func (b *Bot) handleFacilityToggle(ctx context.Context, user domain.User, state *flow, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 {
		return
	}

	mode := parts[1]

	page, err := strconv.Atoi(parts[2])
	if err != nil || page < 0 {
		page = 0
	}

	id, err := strconv.Atoi(parts[3])
	if err != nil {
		return
	}

	facility, err := b.findFacilityByID(ctx, state, id)
	if err != nil {
		return
	}

	if facilityIDSelected(state.Watch.FacilityIDs, facility.ID) {
		state.Watch.FacilityIDs, state.Watch.FacilityNames = removeFacilitySelection(state.Watch.FacilityIDs, state.Watch.FacilityNames, facility.ID)
	} else {
		state.Watch.FacilityIDs = append(state.Watch.FacilityIDs, facility.ID)
		state.Watch.FacilityNames = append(state.Watch.FacilityNames, facility.Name)
		if err := b.db.SaveFavoriteFacility(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, facility); err != nil {
			b.logger.Warn("save favorite facility failed")
		}
	}

	if len(state.Watch.FacilityIDs) > 0 {
		state.Watch.FacilityMode = domain.FacilityModeSelected
	} else {
		state.Watch.FacilityMode = ""
	}

	b.setFlow(user.ID, state)
	b.showFacilityPage(ctx, user, state, mode, page)
}

func (b *Bot) finishSelectedFacilities(ctx context.Context, user domain.User, state *flow) {
	if len(state.Watch.FacilityIDs) == 0 {
		b.send(user, b.catalog.T(user.Locale, "flow.watch.facility_validation"), facilityMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}

	state.Watch.FacilityMode = domain.FacilityModeSelected
	b.setFlow(user.ID, state)

	b.showDateStep(user, state)
}

func (b *Bot) findFacilityByID(ctx context.Context, state *flow, id int) (domain.Facility, error) {
	facilities, err := b.db.Facilities(ctx, state.Watch.CityID, state.Watch.ServiceID, 0)
	if err != nil {
		return domain.Facility{}, err
	}

	for _, facility := range facilities {
		if facility.ID == id {
			return facility, nil
		}
	}

	return domain.Facility{}, fmt.Errorf("facility %d not found", id)
}

func (b *Bot) sendFacilityOptions(user domain.User, title string, facilities []domain.Facility, markup tgbotapi.InlineKeyboardMarkup) {
	state := b.getFlow(user.ID)

	var selectedIDs []int
	if state != nil {
		selectedIDs = state.Watch.FacilityIDs
	}

	var lines []string
	lines = append(lines, strings.TrimSpace(title))

	for i, facility := range facilities {
		prefix := "⬜"
		if facilityIDSelected(selectedIDs, facility.ID) {
			prefix = "✅"
		}

		label := facility.Name
		if facility.Address != "" {
			label += ", " + facility.Address
		}

		lines = append(lines, fmt.Sprintf("%s %d. %s\n   ID: %d", prefix, i+1, label, facility.ID))
	}

	b.sendWatchStep(user, strings.Join(lines, "\n\n"), markup)
}

func facilityIDSelected(ids []int, id int) bool {
	for _, selectedID := range ids {
		if selectedID == id {
			return true
		}
	}

	return false
}

func removeFacilitySelection(ids []int, names []string, id int) ([]int, []string) {
	nextIDs := make([]int, 0, len(ids))
	nextNames := make([]string, 0, len(names))

	for i, selectedID := range ids {
		if selectedID == id {
			continue
		}

		nextIDs = append(nextIDs, selectedID)

		if i < len(names) {
			nextNames = append(nextNames, names[i])
		}
	}

	return nextIDs, nextNames
}

func (b *Bot) ensureCities(ctx context.Context, user domain.User) ([]domain.City, error) {
	cities, err := b.db.Cities(ctx, 24)
	if err != nil {
		return nil, err
	}
	if len(cities) > 0 {
		return cities, nil
	}
	client, err := b.authenticatedLuxMedClient(ctx, user)
	if err != nil {
		return nil, err
	}
	cities, err = client.GetCities(ctx)
	if err != nil {
		return nil, err
	}
	if err := b.db.UpsertCities(ctx, cities); err != nil {
		return nil, err
	}
	return b.db.Cities(ctx, 24)
}

func (b *Bot) ensureProcedures(ctx context.Context, user domain.User, state *flow) error {
	if state.CatalogLoaded {
		return nil
	}
	city := domain.City{ID: state.Watch.CityID, Name: state.Watch.CityName}
	client, err := b.authenticatedLuxMedClient(ctx, user)
	if err != nil {
		return err
	}
	catalog, err := client.GetServices(ctx)
	if err != nil {
		return err
	}
	procedures := make([]domain.Procedure, 0, len(catalog))
	for _, service := range catalog {
		procedures = append(procedures, domain.Procedure{ID: service.ID, Name: service.Name})
	}
	if err := b.db.UpsertProcedures(ctx, city, procedures); err != nil {
		return err
	}
	state.CatalogLoaded = true
	return nil
}

func (b *Bot) handleProcedureText(ctx context.Context, user domain.User, state *flow, text string) {
	if err := b.ensureProcedures(ctx, user, state); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), procedureMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		b.showProcedureMenu(user, state)
		return
	}

	if id, _, ok := parseIDName(text); ok {
		proc, err := b.db.Procedure(ctx, state.Watch.CityID, id)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureSearchKeyboard(b.catalog, user.Locale))
			return
		}

		b.selectProcedure(user, state, proc)
		return
	}

	if id, err := strconv.Atoi(text); err == nil && id > 0 {
		proc, err := b.db.Procedure(ctx, state.Watch.CityID, id)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureSearchKeyboard(b.catalog, user.Locale))
			return
		}

		b.selectProcedure(user, state, proc)
		return
	}

	b.showProcedureSearchResults(ctx, user, state, text)
}

func (b *Bot) selectProcedure(user domain.User, state *flow, proc domain.Procedure) {
	state.Watch.ServiceID, state.Watch.ServiceName = proc.ID, proc.Name
	state.Watch.DoctorMode, state.Watch.DoctorID, state.Watch.DoctorName = domain.DoctorModeAny, nil, ""
	state.Watch.FacilityMode, state.Watch.FacilityIDs, state.Watch.FacilityNames = domain.FacilityModeAll, nil, nil
	state.Doctors = nil
	state.Kind = flowWatchDoctor
	b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.doctor"), doctorModeKeyboard(b.catalog, user.Locale))
}

func (b *Bot) showProcedureLetters(ctx context.Context, user domain.User, state *flow) {
	if err := b.ensureProcedures(ctx, user, state); err != nil {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "errors.generic"), procedureMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}
	letters, err := b.db.ProcedureLetters(ctx, state.Watch.CityID)
	if err != nil || len(letters) == 0 {
		b.logger.Warn(
			"procedure letters not found",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"err", err,
		)
		b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureSearchKeyboard(b.catalog, user.Locale))
		return
	}

	state.Kind = flowWatchService
	state.ProcedureSearchQuery = ""
	b.setFlow(user.ID, state)

	b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.service_letter"), procedureLettersKeyboard(letters, b.catalog, user.Locale))
}

func (b *Bot) handleProcedureLetterPage(ctx context.Context, user domain.User, state *flow, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return
	}

	letter := parts[1]
	page, err := strconv.Atoi(parts[2])
	if err != nil || page < 0 {
		page = 0
	}

	b.showProcedureLetterPage(ctx, user, state, letter, page)
}

func (b *Bot) showProcedureLetterPage(ctx context.Context, user domain.User, state *flow, letter string, page int) {
	const pageSize = 10

	page = min(max(page, 0), 10000)

	offset := page * pageSize
	procedures, err := b.db.ProceduresByLetter(ctx, state.Watch.CityID, letter, pageSize+1, offset)

	b.logger.Info(
		"show procedure letter page",
		"user_id", user.ID,
		"city_id", state.Watch.CityID,
		"city_name", state.Watch.CityName,
		"letter", letter,
		"page", page,
		"procedures_count", len(procedures),
		"err", err,
	)

	if err != nil || len(procedures) == 0 {
		b.send(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureSearchKeyboard(b.catalog, user.Locale))
		return
	}

	hasNext := len(procedures) > pageSize
	if hasNext {
		procedures = procedures[:pageSize]
	}

	previousCallback := ""
	if page > 0 {
		previousCallback = fmt.Sprintf("procletter:%s:%d", letter, page-1)
	}

	nextCallback := ""
	if hasNext {
		nextCallback = fmt.Sprintf("procletter:%s:%d", letter, page+1)
	}

	title := b.catalog.T(user.Locale, "flow.watch.service_letter_results", letter)

	b.sendProcedureOptions(user, title, procedures, procedureResultsKeyboard(procedures, previousCallback, nextCallback, b.catalog, user.Locale))
}

func (b *Bot) handleProcedureSearchPage(ctx context.Context, user domain.User, state *flow, data string) {
	pageText := strings.TrimPrefix(data, "procsearchpage:")
	page, err := strconv.Atoi(pageText)
	if err != nil || page < 0 {
		page = 0
	}

	query := strings.TrimSpace(state.ProcedureSearchQuery)
	if query == "" {
		b.sendWatchStep(user, b.catalog.T(user.Locale, "flow.watch.service_search"), procedureSearchKeyboard(b.catalog, user.Locale))
		return
	}

	b.showProcedureSearchResultsPage(ctx, user, state, query, page)
}

func (b *Bot) showProcedureSearchResultsPage(ctx context.Context, user domain.User, state *flow, query string, page int) {
	const pageSize = 10

	page = min(max(page, 0), 10000)

	offset := page * pageSize
	procedures, err := procedures.Search(ctx, b.db, state.Watch.CityID, user.Locale, query, pageSize+1, offset)

	b.logger.Info(
		"search procedures page",
		"user_id", user.ID,
		"city_id", state.Watch.CityID,
		"city_name", state.Watch.CityName,
		"page", page,
		"procedures_count", len(procedures),
		"err", err,
	)

	if err != nil || len(procedures) == 0 {
		b.send(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureSearchKeyboard(b.catalog, user.Locale))
		return
	}

	hasNext := len(procedures) > pageSize
	if hasNext {
		procedures = procedures[:pageSize]
	}

	previousCallback := ""
	if page > 0 {
		previousCallback = fmt.Sprintf("procsearchpage:%d", page-1)
	}

	nextCallback := ""
	if hasNext {
		nextCallback = fmt.Sprintf("procsearchpage:%d", page+1)
	}

	state.Kind = flowWatchSearch
	state.ProcedureSearchQuery = query
	b.setFlow(user.ID, state)

	title := b.catalog.T(user.Locale, "flow.watch.service_results")

	b.sendProcedureOptions(user, title, procedures, procedureResultsKeyboard(procedures, previousCallback, nextCallback, b.catalog, user.Locale))
}

func (b *Bot) sendProcedureOptions(user domain.User, title string, items []domain.Procedure, markup tgbotapi.InlineKeyboardMarkup) {
	var lines []string
	lines = append(lines, strings.TrimSpace(title))

	for i, proc := range items {
		lines = append(lines, fmt.Sprintf("%d. %s\n   ID: %d", i+1, proc.Name, proc.ID))
	}

	b.sendWatchStep(user, strings.Join(lines, "\n\n"), markup)
}

func (b *Bot) ensureFacilities(ctx context.Context, user domain.User, city domain.City, procedureID int) error {
	existing, err := b.db.Facilities(ctx, city.ID, procedureID, 1)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	client, err := b.authenticatedLuxMedClient(ctx, user)
	if err != nil {
		return err
	}
	data, err := client.GetDoctorsAndFacilities(ctx, city.ID, procedureID)
	if err != nil {
		return err
	}
	return b.db.UpsertFacilities(ctx, city, procedureID, data.Facilities)
}

func (b *Bot) authenticatedLuxMedClient(ctx context.Context, user domain.User) (luxmed.Client, error) {
	if b.cfg.Browsers != nil {
		if err := b.checkAccount(ctx, user); err != nil {
			return nil, err
		}
		return b.cfg.Browsers[user.TelegramUserID].Client(), nil
	}
	account, err := b.db.LuxMedAccount(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	password, err := b.cryptor.Decrypt(account.EncryptedPassword)
	if err != nil {
		return nil, err
	}
	client := b.newClient(false)
	if err := client.Authenticate(ctx, luxmed.Credentials{Login: account.Login, Password: password}); err != nil {
		return nil, err
	}
	return client, nil
}

func (b *Bot) handleWatchAction(ctx context.Context, user domain.User, data string) {
	parts := strings.Split(data, ":")
	if !user.IsActive() || len(parts) != 3 {
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return
	}
	watch, err := b.db.Watch(ctx, id)
	if err != nil || watch.UserID != user.ID || watch.Status == domain.WatchStatusDeleted {
		b.send(user, b.catalog.T(user.Locale, "errors.no_watches"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	var state domain.WatchStatus
	switch parts[1] {
	case "pause":
		state = domain.WatchStatusPaused
	case "resume":
		if err := b.checkAccount(ctx, user); err != nil {
			key := accountCheckMessage(err)
			markup := b.withAccountRecovery(user, key, watchKeyboard(b.catalog, user.Locale, watch))
			b.send(user, b.catalog.T(user.Locale, key), markup)
			return
		}
		state = domain.WatchStatusActive
	case "delete":
		state = domain.WatchStatusDeleted
	case "check":
		select {
		case b.manualChecks <- struct{}{}:
			b.manualWorkers.Go(func() {
				defer func() { <-b.manualChecks }()
				b.checkWatchNow(ctx, user, watch)
			})
		default:
			b.send(user, b.catalog.T(user.Locale, "watch.check_rate_limited"), watchKeyboard(b.catalog, user.Locale, watch))
		}
		return
	case "edit":
		b.editWatch(ctx, user, watch)
		return
	case "history":
		b.showHistoryPage(ctx, user, watch.ID, 0)
		return

	case "show":
		b.showWatch(ctx, user, watch)
		return
	default:
		return
	}
	if err := b.db.SetUserWatchStatus(ctx, user.ID, id, state); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
		return
	}
	key := map[domain.WatchStatus]string{domain.WatchStatusPaused: "watch.paused", domain.WatchStatusActive: "watch.resumed", domain.WatchStatusDeleted: "watch.deleted"}[state]
	b.send(user, b.catalog.T(user.Locale, key), mainKeyboard(b.catalog, user.Locale))
}

func (b *Bot) checkWatchNow(ctx context.Context, user domain.User, watch domain.Watch) {
	key, count := "watch.check_failed", 0
	if b.runner != nil {
		var err error
		count, err = b.runner.ManualCheck(ctx, watch)
		switch {
		case err == nil:
			key = "watch.checked"
		case errors.Is(err, scheduler.ErrRateLimited):
			key = "watch.check_rate_limited"
		case errors.Is(err, scheduler.ErrInactive):
			key = "watch.check_inactive"
		case errors.Is(err, scheduler.ErrNoAccount):
			key = "errors.no_account"
		case errors.Is(err, luxmed.ErrAuthentication):
			key = "errors.needs_login"
		case errors.Is(err, luxmed.ErrPartialResults):
			key = "watch.check_partial"
		case errors.Is(err, luxmed.ErrBrowserBusy):
			key = "watch.browser_busy"
		case errors.Is(err, luxmed.ErrUnavailable):
			key = "errors.luxmed_unavailable"
		}
	}
	if key == "watch.checked" || key == "watch.check_partial" {
		b.send(user, b.catalog.T(user.Locale, key, count), watchKeyboard(b.catalog, user.Locale, watch))
		return
	}
	markup := b.withAccountRecovery(user, key, watchKeyboard(b.catalog, user.Locale, watch))
	b.send(user, b.catalog.T(user.Locale, key), markup)
}

func (b *Bot) showMenu(user domain.User) {
	if !user.IsActive() {
		b.send(user, b.catalog.T(user.Locale, "errors.invite_required"), nil)
		return
	}
	b.send(user, b.catalog.T(user.Locale, "menu.main.title"), mainKeyboard(b.catalog, user.Locale))
}

func (b *Bot) showWatches(ctx context.Context, user domain.User) {
	watches, err := b.db.WatchesByUser(ctx, user.ID)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	if len(watches) == 0 {
		b.send(user, b.catalog.T(user.Locale, "errors.no_watches"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	for _, watch := range watches {
		b.showWatch(ctx, user, watch)
	}
}

func (b *Bot) showWatch(ctx context.Context, user domain.User, watch domain.Watch) {
	count, err := b.db.CountHistoryForWatch(ctx, watch.ID)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	last := "–"
	if watch.LastCheckedAt != nil {
		last = appointmentDateTime(user.Locale, *watch.LastCheckedAt)
	}
	text := b.watchSummary(user, watch) + "\n" + b.catalog.T(user.Locale, "watch.status.checked",
		b.catalog.T(user.Locale, "watch.status."+string(watch.Status)), last, count)
	markup := watchKeyboard(b.catalog, user.Locale, watch)
	if watch.LastError != "" {
		key := "watch.check_failed"
		switch watch.LastError {
		case "luxmed_account_missing":
			key = "errors.no_account"
		case "luxmed_authentication":
			key = "errors.needs_login"
		case "luxmed_unavailable":
			key = "errors.luxmed_unavailable"
		case "luxmed_partial":
			key = "errors.luxmed_partial"
		}
		text += "\n" + b.catalog.T(user.Locale, key)
		markup = b.withAccountRecovery(user, key, markup)
	}
	b.send(user, text, markup)
}

func (b *Bot) showHistory(ctx context.Context, user domain.User) {
	b.showHistoryPage(ctx, user, 0, 0)
}

func (b *Bot) tryRedeemInvite(ctx context.Context, user domain.User, code string) {
	if user.Status != domain.UserStatusPendingInvite || code == "" {
		return
	}
	ok, err := b.db.RedeemInviteForUser(ctx, code, user.ID)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
		return
	}
	if !ok {
		b.send(user, b.catalog.T(user.Locale, "flow.invite.invalid"), nil)
		return
	}
	b.clearFlow(user.ID)
	user.Status = domain.UserStatusActive
	b.showStart(ctx, user, true)
}

func (b *Bot) createInvite(ctx context.Context, user domain.User, args string) {
	if !user.IsAdmin() || !b.cfg.AdminTelegramIDs[user.TelegramUserID] {
		b.showMenu(user)
		return
	}
	maxUses := 1
	if strings.TrimSpace(args) != "" {
		if parsed, err := strconv.Atoi(strings.Fields(args)[0]); err == nil && parsed > 0 {
			maxUses = parsed
		}
	}
	code := randomCode()
	if err := b.db.CreateInvite(ctx, code, user.ID, maxUses, nil); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
		return
	}
	b.send(user, b.catalog.T(user.Locale, "flow.invite.code", code), nil)
}

func (b *Bot) send(user domain.User, text string, markup any) {
	msg := tgbotapi.NewMessage(user.TelegramChatID, truncateMessage(text, 4000))
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	if _, err := b.api.Send(msg); err != nil {
		b.logger.Warn("send Telegram message failed")
	}
}

func (b *Bot) setFlow(userID int64, state *flow) {
	b.flowState[userID] = state
}

func (b *Bot) getFlow(userID int64) *flow {
	return b.flowState[userID]
}

func (b *Bot) clearFlow(userID int64) {
	delete(b.flowState, userID)
}

func parseIDName(text string) (int, string, bool) {
	parts := strings.SplitN(text, "|", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	id, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	name := strings.TrimSpace(parts[1])
	return id, name, err == nil && id > 0 && name != ""
}

func randomCode() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return strings.TrimRight(base64.URLEncoding.EncodeToString(buf), "=")
}
