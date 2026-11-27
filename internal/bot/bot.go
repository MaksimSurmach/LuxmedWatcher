package bot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

type Config struct {
	DefaultLocale    string
	AdminTelegramIDs map[int64]bool
	MinimumInterval  time.Duration
	PollTimeout      int
}

type Bot struct {
	api       *tgbotapi.BotAPI
	db        *storage.DB
	catalog   *i18n.Catalog
	cryptor   *security.Cryptor
	cfg       Config
	runner    *scheduler.Runner
	logger    *slog.Logger
	flowMu    sync.Mutex
	flowState map[int64]*flow
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
	flowWatchFacility flowKind = "watch_facility"
	flowWatchTime     flowKind = "watch_time"
)

type flow struct {
	Kind                 flowKind
	Login                string
	CitySettings         bool
	ProcedureSearchQuery string
	Watch                domain.Watch
}

func New(api *tgbotapi.BotAPI, db *storage.DB, catalog *i18n.Catalog, cryptor *security.Cryptor, cfg Config, logger *slog.Logger) *Bot {
	return &Bot{api: api, db: db, catalog: catalog, cryptor: cryptor, cfg: cfg, logger: logger, flowState: make(map[int64]*flow)}
}

func (b *Bot) SetRunner(runner *scheduler.Runner) {
	b.runner = runner
}

func (b *Bot) Run(ctx context.Context) error {
	b.registerCommands()
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = b.cfg.PollTimeout
	updates := b.api.GetUpdatesChan(updateConfig)
	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return nil
		case update := <-updates:
			if update.UpdateID == 0 {
				continue
			}
			go b.handleUpdate(ctx, update)
		}
	}
}

func (b *Bot) NotifyAppointment(ctx context.Context, user domain.User, watch domain.Watch, history domain.AppointmentHistory) (int, error) {
	return b.NotifyAppointments(ctx, user, watch, []domain.AppointmentHistory{history}, 1)
}

func (b *Bot) NotifyAppointments(ctx context.Context, user domain.User, watch domain.Watch, histories []domain.AppointmentHistory, totalFound int) (int, error) {
	if len(histories) == 0 {
		return 0, nil
	}

	shown := len(histories)
	if totalFound < shown {
		totalFound = shown
	}

	messageText := appointmentsHTML(b.tr(user), watch, histories, totalFound)

	first := histories[0]
	msg := tgbotapi.NewMessage(user.TelegramChatID, messageText)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.open_luxmed"), first.BookingURL)),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.check_again"), fmt.Sprintf("watch:check:%d", watch.ID)),
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.show_watch"), fmt.Sprintf("watch:show:%d", watch.ID)),
		),
	)

	sent, err := b.api.Send(msg)
	return sent.MessageID, err
}

// NotifyAuthFailed tells the user LuxMed rejected their saved login, with a way to fix it.
func (b *Bot) NotifyAuthFailed(user domain.User) {
	b.send(user, b.catalog.T(user.Locale, "errors.auth_failed"), tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.account"), "account")),
	))
}

func (b *Bot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		b.handleCallback(ctx, update.CallbackQuery)
		return
	}
	if update.Message == nil || update.Message.From == nil {
		return
	}
	user, err := b.upsertUser(ctx, update.Message)
	if err != nil {
		b.logger.Warn("upsert user failed", "err", err)
		return
	}
	if update.Message.IsCommand() {
		b.handleCommand(ctx, user, update.Message)
		return
	}
	if state := b.getFlow(user.ID); state != nil && (state.Kind == flowAccountLogin || state.Kind == flowAccountPass) {
		// Credentials must not stay in Telegram's chat history.
		_, _ = b.api.Request(tgbotapi.NewDeleteMessage(update.Message.Chat.ID, update.Message.MessageID))
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
	if b.cfg.AdminTelegramIDs[msg.From.ID] && (!user.IsActive() || !user.IsAdmin()) {
		if err := b.db.SetUserActive(ctx, user.ID, domain.UserRoleAdmin); err != nil {
			return domain.User{}, err
		}
		user.Status = domain.UserStatusActive
		user.Role = domain.UserRoleAdmin
	}
	return user, nil
}

func (b *Bot) handleCommand(ctx context.Context, user domain.User, msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		arg := strings.TrimSpace(msg.CommandArguments())
		if arg != "" && !user.IsActive() {
			b.tryRedeemInvite(ctx, user, arg)
			return
		}
		if !user.IsActive() {
			b.setFlow(user.ID, &flow{Kind: flowInvite})
			b.send(user, b.catalog.T(user.Locale, "flow.invite.enter"), languageKeyboard())
			return
		}
		b.showMenu(user)
	case "menu":
		b.showMenu(user)
	case "new":
		b.startWatchFlow(ctx, user)
	case "watches":
		b.showWatches(ctx, user)
	case "history":
		b.showHistory(ctx, user)
	case "settings":
		b.send(user, b.catalog.T(user.Locale, "settings.title"), settingsKeyboard(b.catalog, user.Locale))
	case "language":
		b.send(user, b.catalog.T(user.Locale, "flow.language.choose"), languageKeyboard())
	case "help":
		b.send(user, b.catalog.T(user.Locale, "help.text"), mainKeyboard(b.catalog, user.Locale))
	case "cancel":
		b.clearFlow(user.ID)
		b.showMenu(user)
	case "invite":
		b.createInvite(ctx, user, msg.CommandArguments())
	default:
		b.showMenu(user)
	}
}

func (b *Bot) handleText(ctx context.Context, user domain.User, text string) {
	state := b.getFlow(user.ID)
	if state == nil || state.Kind == flowNone {
		if !user.IsActive() {
			b.tryRedeemInvite(ctx, user, text)
			return
		}
		b.showMenu(user)
		return
	}
	switch state.Kind {
	case flowInvite:
		b.tryRedeemInvite(ctx, user, text)
	case flowAccountLogin:
		state.Login = text
		state.Kind = flowAccountPass
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.account.password"), cancelKeyboard(b.catalog, user.Locale))
	case flowAccountPass:
		authErr := luxmed.NewHTTPClient(false).Authenticate(ctx, luxmed.Credentials{Login: state.Login, Password: text})
		if errors.Is(authErr, luxmed.ErrAuthRejected) {
			state.Kind = flowAccountLogin
			b.setFlow(user.ID, state)
			b.send(user, b.catalog.T(user.Locale, "errors.auth_failed")+"\n\n"+b.catalog.T(user.Locale, "flow.account.login"), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		encrypted, err := b.cryptor.Encrypt(text)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
			return
		}
		// A network failure here still saves: the scheduler re-checks the login on every run.
		var lastLogin *time.Time
		if authErr == nil {
			now := time.Now().UTC()
			lastLogin = &now
		}
		err = b.db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
			UserID:            user.ID,
			Login:             state.Login,
			EncryptedPassword: encrypted,
			LastLoginAt:       lastLogin,
			Status:            domain.AccountStatusActive,
		})
		b.clearFlow(user.ID)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
			return
		}
		b.send(user, b.catalog.T(user.Locale, "flow.account.saved"), mainKeyboard(b.catalog, user.Locale))
	case flowWatchCity:
		b.showCityPicker(ctx, user, false, 0)
	case flowCitySearch:
		b.showCitySearchResults(ctx, user, state, text)
	case flowWatchService:
		b.handleProcedureText(ctx, user, state, text)
	case flowWatchSearch:
		b.handleProcedureText(ctx, user, state, text)
	case flowWatchFacility:
		b.handleFacilityText(ctx, user, state, text)
	case flowWatchTime:
		b.handleTimeText(ctx, user, state, text)
	}
}

func (b *Bot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || cb.From == nil {
		return
	}
	user, err := b.db.UserByTelegramID(ctx, cb.From.ID)
	if err != nil {
		return
	}
	data := cb.Data
	toast := ""
	// Flow state lives in memory; after a restart or Cancel these buttons point at nothing.
	expired := isFlowCallback(data) && b.getFlow(user.ID) == nil
	switch {
	case expired:
		toast = b.catalog.T(user.Locale, "toast.expired")
	case strings.HasPrefix(data, "watch:check:"):
		toast = b.catalog.T(user.Locale, "toast.checking")
	}
	_, _ = b.api.Request(tgbotapi.NewCallback(cb.ID, toast))
	// Callback data is client-controlled: until invited, only language choice is allowed,
	// otherwise the account flow becomes a free LuxMed credential checker.
	if !user.IsActive() && !strings.HasPrefix(data, "lang:") {
		return
	}
	if expired {
		b.showMenu(user)
		return
	}
	if replacesMessage(data) {
		// Paging and toggles redraw the whole list; drop the old copy instead of stacking them.
		_, _ = b.api.Request(tgbotapi.NewDeleteMessage(user.TelegramChatID, cb.Message.MessageID))
	}
	switch {
	case strings.HasPrefix(data, "lang:"):
		locale := strings.TrimPrefix(data, "lang:")
		_ = b.db.SetUserLocale(ctx, user.ID, locale)
		user.Locale = locale
		if !user.IsActive() {
			b.setFlow(user.ID, &flow{Kind: flowInvite})
			b.send(user, b.catalog.T(user.Locale, "flow.invite.enter"), nil)
			return
		}
		b.send(user, b.catalog.T(user.Locale, "flow.language.saved"), mainKeyboard(b.catalog, user.Locale))
	case data == "menu":
		b.showMenu(user)
	case data == "account":
		b.setFlow(user.ID, &flow{Kind: flowAccountLogin})
		b.send(user, b.catalog.T(user.Locale, "flow.account.login"), cancelKeyboard(b.catalog, user.Locale))
	case data == "new":
		b.startWatchFlow(ctx, user)
	case data == "watches":
		b.showWatches(ctx, user)
	case data == "history":
		b.showHistory(ctx, user)
	case data == "settings":
		b.send(user, b.catalog.T(user.Locale, "settings.title"), settingsKeyboard(b.catalog, user.Locale))
	case data == "settings:language":
		b.send(user, b.catalog.T(user.Locale, "flow.language.choose"), languageKeyboard())
	case data == "settings:city":
		b.setFlow(user.ID, &flow{Kind: flowWatchCity, CitySettings: true})
		b.showCityPicker(ctx, user, true, 0)
	case data == "cancel":
		b.clearFlow(user.ID)
		b.showMenu(user)
	case data == "city:search":
		state := b.getFlow(user.ID)
		if state == nil {
			state = &flow{Kind: flowCitySearch}
		}
		state.Kind = flowCitySearch
		state.CitySettings = false
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.city_search"), citySearchKeyboard(b.catalog, user.Locale))
	case data == "prefcity:search":
		b.setFlow(user.ID, &flow{Kind: flowCitySearch, CitySettings: true})
		b.send(user, b.catalog.T(user.Locale, "flow.city_search"), citySearchKeyboard(b.catalog, user.Locale))
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
		if state == nil {
			return
		}
		state.Kind = flowWatchSearch
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.watch.service_search"), procedureSearchKeyboard(b.catalog, user.Locale))
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
			b.showTimeStep(user, state)
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
		state := b.getFlow(user.ID)
		if state != nil {
			b.handleTimeCallback(ctx, user, state, data)
		}
	case strings.HasPrefix(data, "interval:"):
		b.finishWatch(ctx, user, data)
	case strings.HasPrefix(data, "fac:"):
		b.handleFacility(ctx, user, data)
	case strings.HasPrefix(data, "watch:"):
		b.handleWatchAction(ctx, user, data)
	}
}

func (b *Bot) startWatchFlow(ctx context.Context, user domain.User) {
	if _, err := b.db.LuxMedAccount(ctx, user.ID); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.no_account"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	state := &flow{Kind: flowWatchCity, Watch: domain.Watch{UserID: user.ID, NextDays: 14, DoctorMode: domain.DoctorModeAny, FacilityMode: domain.FacilityModeAll}}
	b.setFlow(user.ID, state)
	if user.PreferredCityID != nil {
		state.Watch.CityID = *user.PreferredCityID
		state.Watch.CityName = user.PreferredCityName
		state.Kind = flowWatchService
		b.setFlow(user.ID, state)
		b.showProcedureMenu(ctx, user, state)
		return
	}
	b.showCityPicker(ctx, user, false, 0)
}

func (b *Bot) showCityPicker(ctx context.Context, user domain.User, settings bool, page int) {
	const pageSize = 12
	if page < 0 {
		page = 0
	}
	if _, err := b.ensureCities(ctx, user); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	cities, err := b.db.CitiesPage(ctx, pageSize+1, page*pageSize)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
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
	b.send(user, b.catalog.T(user.Locale, "flow.watch.city"), cityKeyboard(cities, prefix, page, page > 0, hasNext, b.catalog, user.Locale))
}

func (b *Bot) showTimeStep(user domain.User, state *flow) {
	state.Kind = flowWatchTime
	b.setFlow(user.ID, state)

	b.send(
		user,
		b.catalog.T(user.Locale, "flow.watch.time"),
		timeWindowKeyboard(b.catalog, user.Locale),
	)
}

func (b *Bot) handleTimeCallback(ctx context.Context, user domain.User, state *flow, data string) {
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
		b.send(user, b.catalog.T(user.Locale, "flow.watch.time_invalid"), timeWindowKeyboard(b.catalog, user.Locale))
		return
	}

	b.finishTimeStep(ctx, user, state)
}

func (b *Bot) handleTimeText(ctx context.Context, user domain.User, state *flow, text string) {
	text = strings.TrimSpace(strings.ToLower(text))

	if text == "" || text == "any" || text == "все" || text == "любое" {
		state.Watch.TimeWindows = nil
		b.finishTimeStep(ctx, user, state)
		return
	}

	window, ok := parseTimeWindow(text)
	if !ok {
		b.send(user, b.catalog.T(user.Locale, "flow.watch.time_invalid"), timeWindowKeyboard(b.catalog, user.Locale))
		return
	}

	state.Watch.TimeWindows = []domain.TimeWindow{window}
	b.finishTimeStep(ctx, user, state)
}

func (b *Bot) finishTimeStep(ctx context.Context, user domain.User, state *flow) {
	b.setFlow(user.ID, state)
	b.send(user, b.catalog.T(user.Locale, "flow.watch.interval"), intervalKeyboard(b.catalog, user.Locale, b.cfg.MinimumInterval))
	_ = ctx
}

// isFlowCallback reports callbacks that only make sense inside a live watch-creation flow.
func isFlowCallback(data string) bool {
	for _, prefix := range []string{"city:", "proc", "fac", "time:", "interval:"} {
		if strings.HasPrefix(data, prefix) {
			return true
		}
	}
	return false
}

func replacesMessage(data string) bool {
	for _, prefix := range []string{"facpage:", "facsel:", "facclear", "procletter:", "procsearchpage:", "city:page:", "prefcity:page:"} {
		if strings.HasPrefix(data, prefix) {
			return true
		}
	}
	return false
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
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	cities, err := b.db.SearchCities(ctx, query, 12)
	if err != nil || len(cities) == 0 {
		b.send(user, b.catalog.T(user.Locale, "flow.city_not_found"), citySearchKeyboard(b.catalog, user.Locale))
		return
	}
	prefix := "city:"
	if state.CitySettings {
		prefix = "prefcity:"
	}
	b.send(user, b.catalog.T(user.Locale, "flow.city_results"), cityKeyboard(cities, prefix, 0, false, false, b.catalog, user.Locale))
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
	_ = b.db.SetUserPreferredCity(ctx, user.ID, city)
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
	state.Watch.CityID = city.ID
	state.Watch.CityName = city.Name
	state.Kind = flowWatchService
	b.setFlow(user.ID, state)
	b.showProcedureMenu(ctx, user, state)
}

func (b *Bot) showProcedureMenu(ctx context.Context, user domain.User, state *flow) {
	if err := b.ensureProcedures(ctx, user, domain.City{ID: state.Watch.CityID, Name: state.Watch.CityName}); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}

	state.Kind = flowWatchService
	state.ProcedureSearchQuery = ""
	b.setFlow(user.ID, state)

	b.refreshRecentProcedures(ctx, user, state)

	recent, err := b.db.RecentProcedures(ctx, state.Watch.CityID, 3)
	if err != nil {
		b.logger.Warn(
			"load recent procedures from db failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"err", err,
		)
	}

	b.logger.Info(
		"show procedure menu",
		"user_id", user.ID,
		"city_id", state.Watch.CityID,
		"city_name", state.Watch.CityName,
		"recent_count", len(recent),
	)

	if len(recent) > 0 {
		title := b.catalog.T(user.Locale, "flow.watch.service") + "\n\n" + b.catalog.T(user.Locale, "flow.watch.service_recent")
		b.sendProcedureOptions(user, title, recent, procedureResultsKeyboard(recent, "", "", b.catalog, user.Locale))
		return
	}

	b.send(
		user,
		b.catalog.T(user.Locale, "flow.watch.service")+"\n\n"+b.catalog.T(user.Locale, "flow.watch.service_hint"),
		procedureMenuKeyboard(nil, b.catalog, user.Locale),
	)
}

func (b *Bot) refreshRecentProcedures(ctx context.Context, user domain.User, state *flow) {
	client, err := b.authenticatedLuxMedClient(ctx, user)
	if err != nil {
		b.logger.Warn(
			"authenticate luxmed client for recent procedures failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"err", err,
		)
		return
	}

	recent, err := client.GetRecentProcedures(ctx)
	if err != nil {
		b.logger.Warn(
			"load recent procedures failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"err", err,
		)
		return
	}

	b.logger.Info(
		"recent procedures loaded from luxmed",
		"user_id", user.ID,
		"city_id", state.Watch.CityID,
		"city_name", state.Watch.CityName,
		"recent_count", len(recent),
	)

	if len(recent) == 0 {
		return
	}

	if err := b.db.UpsertProcedures(ctx, domain.City{ID: state.Watch.CityID, Name: state.Watch.CityName}, recent); err != nil {
		b.logger.Warn(
			"upsert recent procedures failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"recent_count", len(recent),
			"err", err,
		)
		return
	}

	dbRecent, err := b.db.RecentProcedures(ctx, state.Watch.CityID, 3)
	if err != nil {
		b.logger.Warn(
			"check recent procedures after upsert failed",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"err", err,
		)
		return
	}

	b.logger.Info(
		"recent procedures saved",
		"user_id", user.ID,
		"city_id", state.Watch.CityID,
		"city_name", state.Watch.CityName,
		"db_recent_count", len(dbRecent),
	)
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

	title := b.catalog.T(user.Locale, "flow.watch.service_popular")
	b.sendProcedureOptions(user, title, popular, procedureResultsKeyboard(popular, "", "", b.catalog, user.Locale))
}

func (b *Bot) showProcedureSearchResults(ctx context.Context, user domain.User, state *flow, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		b.send(user, b.catalog.T(user.Locale, "flow.watch.service_search"), procedureSearchKeyboard(b.catalog, user.Locale))
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

	b.selectProcedure(ctx, user, state, proc)
}

func (b *Bot) showFacilityMenu(ctx context.Context, user domain.User, state *flow) {
	city := domain.City{ID: state.Watch.CityID, Name: state.Watch.CityName}
	if err := b.ensureFacilities(ctx, user, city, state.Watch.ServiceID); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
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
		title := b.catalog.T(user.Locale, "flow.watch.facility") + "\n\n" + b.catalog.T(user.Locale, "facility.title_favorites") + ":"
		b.sendFacilityOptions(
			user,
			title,
			favorites,
			facilityResultsKeyboard(favorites, "favs", 0, "", "", state.Watch.FacilityIDs, b.catalog, user.Locale),
		)
		return
	}

	b.send(
		user,
		b.catalog.T(user.Locale, "flow.watch.facility")+"\n\n"+b.catalog.T(user.Locale, "flow.watch.facility_hint"),
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

	_ = b.db.SaveFavoriteFacility(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, facility)

	b.setFlow(user.ID, state)
	b.showTimeStep(user, state)
}

func (b *Bot) handleFacilityText(ctx context.Context, user domain.User, state *flow, text string) {
	text = strings.TrimSpace(text)

	if strings.EqualFold(text, "all") || strings.EqualFold(text, "все") {
		state.Watch.FacilityMode = domain.FacilityModeAll
		state.Watch.FacilityIDs = nil
		state.Watch.FacilityNames = nil
		b.setFlow(user.ID, state)
		b.showTimeStep(user, state)
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

	_ = b.db.SaveFavoriteFacility(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, facility)

	b.setFlow(user.ID, state)
	b.showTimeStep(user, state)
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

	if page < 0 {
		page = 0
	}

	offset := page * pageSize

	var (
		facilities []domain.Facility
		err        error
		title      string
	)

	switch mode {
	case "favs":
		facilities, err = b.db.FavoriteFacilitiesPage(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, pageSize+1, offset)
		title = b.catalog.T(user.Locale, "facility.title_favorites")
	default:
		mode = "all"
		facilities, err = b.db.FacilitiesPage(ctx, state.Watch.CityID, state.Watch.ServiceID, pageSize+1, offset)
		title = b.catalog.T(user.Locale, "facility.title_all")
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

	text := b.catalog.T(user.Locale,
		"flow.watch.facility_page",
		title,
		offset+1,
		offset+len(facilities),
	)

	if len(state.Watch.FacilityIDs) > 0 {
		text += "\n\n" + b.catalog.T(user.Locale, "flow.watch.facility_selected", len(state.Watch.FacilityIDs))
	}

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
		_ = b.db.SaveFavoriteFacility(ctx, user.ID, state.Watch.CityID, state.Watch.ServiceID, facility)
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
		b.send(user, b.catalog.T(user.Locale, "flow.watch.facility_none_selected"), facilityMenuKeyboard(nil, b.catalog, user.Locale))
		return
	}

	state.Watch.FacilityMode = domain.FacilityModeSelected
	b.setFlow(user.ID, state)

	b.showTimeStep(user, state)
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

func (b *Bot) sendFacilityOptions(user domain.User, title string, facilities []domain.Facility, markup any) {
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

		lines = append(lines, fmt.Sprintf("%s %d. %s", prefix, i+1, label))
	}

	b.send(user, strings.Join(lines, "\n\n"), markup)
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

func (b *Bot) ensureProcedures(ctx context.Context, user domain.User, city domain.City) error {
	existing, err := b.db.SearchProcedures(ctx, city.ID, "", 1)
	if err != nil {
		b.logger.Warn(
			"check existing procedures failed",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
			"err", err,
		)
		return err
	}

	b.logger.Info(
		"ensure procedures started",
		"user_id", user.ID,
		"city_id", city.ID,
		"city_name", city.Name,
		"existing_count", len(existing),
	)

	if len(existing) > 0 {
		b.logger.Info(
			"procedures already cached",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
		)
		return nil
	}

	client, err := b.authenticatedLuxMedClient(ctx, user)
	if err != nil {
		b.logger.Warn(
			"authenticate luxmed client for procedures failed",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
			"err", err,
		)
		return err
	}

	services, err := client.GetServices(ctx)
	if err != nil {
		b.logger.Warn(
			"load luxmed services failed",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
			"err", err,
		)
		return err
	}

	b.logger.Info(
		"luxmed services parsed",
		"user_id", user.ID,
		"city_id", city.ID,
		"city_name", city.Name,
		"services_count", len(services),
	)

	procedures := make([]domain.Procedure, 0, len(services))
	for _, service := range services {
		procedures = append(procedures, domain.Procedure{
			ID:   service.ID,
			Name: service.Name,
		})
	}

	recent, err := client.GetRecentProcedures(ctx)
	if err != nil {
		b.logger.Warn(
			"load recent procedures failed",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
			"err", err,
		)
	} else {
		b.logger.Info(
			"recent procedures parsed",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
			"recent_count", len(recent),
		)
		procedures = append(procedures, recent...)
	}

	if len(procedures) == 0 {
		b.logger.Warn(
			"no procedures parsed before upsert",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
		)
	}

	if err := b.db.UpsertProcedures(ctx, city, procedures); err != nil {
		b.logger.Warn(
			"upsert procedures failed",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
			"procedures_count", len(procedures),
			"err", err,
		)
		return err
	}

	after, err := b.db.SearchProcedures(ctx, city.ID, "", 3)
	if err != nil {
		b.logger.Warn(
			"check procedures after upsert failed",
			"user_id", user.ID,
			"city_id", city.ID,
			"city_name", city.Name,
			"err", err,
		)
		return err
	}

	b.logger.Info(
		"procedures upserted",
		"user_id", user.ID,
		"city_id", city.ID,
		"city_name", city.Name,
		"procedures_count", len(procedures),
		"db_sample_count", len(after),
	)

	return nil
}

func (b *Bot) handleProcedureText(ctx context.Context, user domain.User, state *flow, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		b.showProcedureMenu(ctx, user, state)
		return
	}

	if id, _, ok := parseIDName(text); ok {
		proc, err := b.db.Procedure(ctx, state.Watch.CityID, id)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureSearchKeyboard(b.catalog, user.Locale))
			return
		}

		b.selectProcedure(ctx, user, state, proc)
		return
	}

	if id, err := strconv.Atoi(text); err == nil && id > 0 {
		proc, err := b.db.Procedure(ctx, state.Watch.CityID, id)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureSearchKeyboard(b.catalog, user.Locale))
			return
		}

		b.selectProcedure(ctx, user, state, proc)
		return
	}

	b.showProcedureSearchResults(ctx, user, state, text)
}

func (b *Bot) selectProcedure(ctx context.Context, user domain.User, state *flow, proc domain.Procedure) {
	state.Watch.ServiceID = proc.ID
	state.Watch.ServiceName = proc.Name
	state.Watch.DoctorMode = domain.DoctorModeAny
	state.Kind = flowWatchFacility

	b.setFlow(user.ID, state)

	b.logger.Info(
		"procedure selected",
		"user_id", user.ID,
		"city_id", state.Watch.CityID,
		"city_name", state.Watch.CityName,
		"procedure_id", proc.ID,
		"procedure_name", proc.Name,
	)

	b.showFacilityMenu(ctx, user, state)
}

func (b *Bot) showProcedureLetters(ctx context.Context, user domain.User, state *flow) {
	letters, err := b.db.ProcedureLetters(ctx, state.Watch.CityID)
	if err != nil || len(letters) == 0 {
		b.logger.Warn(
			"procedure letters not found",
			"user_id", user.ID,
			"city_id", state.Watch.CityID,
			"city_name", state.Watch.CityName,
			"err", err,
		)
		b.send(user, b.catalog.T(user.Locale, "flow.watch.service_not_found"), procedureSearchKeyboard(b.catalog, user.Locale))
		return
	}

	state.Kind = flowWatchService
	state.ProcedureSearchQuery = ""
	b.setFlow(user.ID, state)

	b.send(user, b.catalog.T(user.Locale, "flow.watch.service_letters"), procedureLettersKeyboard(letters, b.catalog, user.Locale))
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

	if page < 0 {
		page = 0
	}

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

	title := b.catalog.T(user.Locale,
		"flow.watch.service_letter_page",
		letter,
		offset+1,
		offset+len(procedures),
	)

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
		b.send(user, b.catalog.T(user.Locale, "flow.watch.service_search"), procedureSearchKeyboard(b.catalog, user.Locale))
		return
	}

	b.showProcedureSearchResultsPage(ctx, user, state, query, page)
}

func (b *Bot) showProcedureSearchResultsPage(ctx context.Context, user domain.User, state *flow, query string, page int) {
	const pageSize = 10

	if page < 0 {
		page = 0
	}

	offset := page * pageSize
	procedures, err := b.db.SearchProceduresPage(ctx, state.Watch.CityID, query, pageSize+1, offset)

	b.logger.Info(
		"search procedures page",
		"user_id", user.ID,
		"city_id", state.Watch.CityID,
		"city_name", state.Watch.CityName,
		"query", query,
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

	title := b.catalog.T(user.Locale,
		"flow.watch.service_search_page",
		query,
		offset+1,
		offset+len(procedures),
	)

	b.sendProcedureOptions(user, title, procedures, procedureResultsKeyboard(procedures, previousCallback, nextCallback, b.catalog, user.Locale))
}

func (b *Bot) sendProcedureOptions(user domain.User, title string, procedures []domain.Procedure, markup any) {
	var lines []string
	lines = append(lines, strings.TrimSpace(title))

	for i, proc := range procedures {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, proc.Name))
	}

	b.send(user, strings.Join(lines, "\n\n"), markup)
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
	account, err := b.db.LuxMedAccount(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	password, err := b.cryptor.Decrypt(account.EncryptedPassword)
	if err != nil {
		return nil, err
	}
	client := luxmed.NewHTTPClient(false)
	if err := client.Authenticate(ctx, luxmed.Credentials{Login: account.Login, Password: password}); err != nil {
		return nil, err
	}
	return client, nil
}

func (b *Bot) finishWatch(ctx context.Context, user domain.User, data string) {
	state := b.getFlow(user.ID)
	if state == nil {
		return
	}
	seconds, _ := strconv.Atoi(strings.TrimPrefix(data, "interval:"))
	min := int(b.cfg.MinimumInterval.Seconds())
	if seconds < min {
		seconds = min
	}
	watch := state.Watch
	watch.CheckIntervalSeconds = seconds
	if watch.Name == "" {
		watch.Name = watch.ServiceName + " - " + watch.CityName
	}
	if watch.FacilityMode == "" {
		watch.FacilityMode = domain.FacilityModeAll
	}
	if watch.DoctorMode == "" {
		watch.DoctorMode = domain.DoctorModeAny
	}
	_, err := b.db.CreateWatch(ctx, watch)
	b.clearFlow(user.ID)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
		return
	}
	b.send(user, b.catalog.T(user.Locale, "flow.watch.created"), mainKeyboard(b.catalog, user.Locale))
}

func (b *Bot) handleWatchAction(ctx context.Context, user domain.User, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return
	}
	// Callback data is client-controlled: every watch action must belong to the caller.
	watch, err := b.db.Watch(ctx, id)
	if err != nil || watch.UserID != user.ID {
		return
	}
	switch parts[1] {
	case "pause":
		_ = b.db.SetWatchStatus(ctx, id, domain.WatchStatusPaused)
		b.send(user, b.catalog.T(user.Locale, "watch.paused"), tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.resume"), fmt.Sprintf("watch:resume:%d", id)),
		)))
	case "resume":
		_ = b.db.SetWatchStatus(ctx, id, domain.WatchStatusActive)
		b.send(user, b.catalog.T(user.Locale, "watch.resumed"), mainKeyboard(b.catalog, user.Locale))
	case "delete":
		b.send(user, b.catalog.T(user.Locale, "watch.delete_confirm", watch.Name), tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.delete"), fmt.Sprintf("watch:deleteyes:%d", id)),
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.keep"), fmt.Sprintf("watch:show:%d", id)),
		)))
	case "deleteyes":
		_ = b.db.SetWatchStatus(ctx, id, domain.WatchStatusDeleted)
		b.send(user, b.catalog.T(user.Locale, "watch.deleted"), mainKeyboard(b.catalog, user.Locale))
	case "check":
		if b.runner == nil {
			return
		}
		go func() { _ = b.runner.CheckWatch(context.Background(), watch) }()
	case "show":
		b.sendWatchCard(ctx, user, watch)
	}
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
	if err != nil || len(watches) == 0 {
		b.send(user, b.catalog.T(user.Locale, "errors.no_watches"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	for _, watch := range watches {
		b.sendWatchCard(ctx, user, watch)
	}
}

func (b *Bot) sendWatchCard(ctx context.Context, user domain.User, watch domain.Watch) {
	count, _ := b.db.CountHistoryForWatch(ctx, watch.ID)
	last := "-"
	if watch.LastCheckedAt != nil {
		last = watch.LastCheckedAt.In(warsaw).Format(appointmentTimeLayout)
	}
	text := b.catalog.T(user.Locale, "watch.list.item", esc(watch.Name), watch.Status, last, count)
	b.sendHTML(user, text, watchKeyboard(b.catalog, user.Locale, watch))
}

func (b *Bot) showHistory(ctx context.Context, user domain.User) {
	items, err := b.db.RecentHistory(ctx, user.ID, 10, 0)
	if err != nil || len(items) == 0 {
		b.send(user, b.catalog.T(user.Locale, "history.empty"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	b.sendHTML(user, historyHTML(b.tr(user), items), mainKeyboard(b.catalog, user.Locale))
}

func (b *Bot) tryRedeemInvite(ctx context.Context, user domain.User, code string) {
	ok, err := b.db.RedeemInvite(ctx, code)
	if err != nil || !ok {
		b.send(user, b.catalog.T(user.Locale, "flow.invite.invalid"), nil)
		return
	}
	_ = b.db.SetUserActive(ctx, user.ID, domain.UserRoleUser)
	b.clearFlow(user.ID)
	user.Status = domain.UserStatusActive
	b.send(user, b.catalog.T(user.Locale, "flow.invite.accepted"), tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.account"), "account"),
	)))
}

func (b *Bot) createInvite(ctx context.Context, user domain.User, args string) {
	if !user.IsAdmin() {
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
	b.send(user, b.catalog.T(user.Locale, "admin.invite_code", fmt.Sprintf("https://t.me/%s?start=%s", b.api.Self.UserName, code)), nil)
}

// registerCommands fills Telegram's / menu in each supported language.
func (b *Bot) registerCommands() {
	for _, locale := range []string{"en", "pl", "ru"} {
		var commands []tgbotapi.BotCommand
		for _, name := range []string{"new", "watches", "history", "settings", "help", "cancel"} {
			commands = append(commands, tgbotapi.BotCommand{Command: name, Description: b.catalog.T(locale, "command."+name)})
		}
		language := locale
		if locale == "en" {
			language = "" // default for every language without its own list
		}
		if _, err := b.api.Request(tgbotapi.NewSetMyCommandsWithScopeAndLanguage(tgbotapi.NewBotCommandScopeDefault(), language, commands...)); err != nil {
			b.logger.Warn("set bot commands failed", "locale", locale, "err", err)
		}
	}
}

func (b *Bot) tr(user domain.User) translate {
	return func(key string, args ...any) string { return b.catalog.T(user.Locale, key, args...) }
}

func (b *Bot) send(user domain.User, text string, markup any) {
	b.sendMode(user, text, markup, "")
}

// sendHTML is for messages whose dynamic parts are already escaped with esc.
func (b *Bot) sendHTML(user domain.User, text string, markup any) {
	b.sendMode(user, text, markup, tgbotapi.ModeHTML)
}

func (b *Bot) sendMode(user domain.User, text string, markup any, parseMode string) {
	msg := tgbotapi.NewMessage(user.TelegramChatID, text)
	msg.ParseMode = parseMode
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	if _, err := b.api.Send(msg); err != nil {
		b.logger.Warn("send telegram message failed", "err", err)
	}
}

func (b *Bot) setFlow(userID int64, state *flow) {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	b.flowState[userID] = state
}

func (b *Bot) getFlow(userID int64) *flow {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	return b.flowState[userID]
}

func (b *Bot) clearFlow(userID int64) {
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
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
