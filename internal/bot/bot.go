package bot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	flowWatchDoctor   flowKind = "watch_doctor"
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
	text := b.catalog.T(user.Locale, "notification.appointment_found.title") + "\n\n" +
		b.catalog.T(user.Locale, "notification.appointment_found.body", watch.Name, history.DateTime.Format("2006-01-02 15:04"), history.DoctorName, history.FacilityName, history.ServiceName)
	msg := tgbotapi.NewMessage(user.TelegramChatID, text)
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.open_luxmed"), history.BookingURL)),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.pause"), fmt.Sprintf("watch:pause:%d", watch.ID)),
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.show_watch"), fmt.Sprintf("watch:show:%d", watch.ID)),
		),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.check_again"), fmt.Sprintf("watch:check:%d", watch.ID))),
	)
	sent, err := b.api.Send(msg)
	return sent.MessageID, err
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
	case "settings", "language":
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
		b.send(user, b.catalog.T(user.Locale, "flow.account.password"), nil)
	case flowAccountPass:
		encrypted, err := b.cryptor.Encrypt(text)
		if err != nil {
			b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
			return
		}
		now := time.Now().UTC()
		err = b.db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
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
		b.showCityPicker(ctx, user, false, 0)
	case flowCitySearch:
		b.showCitySearchResults(ctx, user, state, text)
	case flowWatchService:
		b.handleProcedureText(ctx, user, state, text)
	case flowWatchSearch:
		b.handleProcedureText(ctx, user, state, text)
	case flowWatchDoctor:
		id, name, ok := parseIDName(text)
		if !ok {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.doctor_exact"), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		state.Watch.DoctorID = &id
		state.Watch.DoctorName = name
		state.Watch.DoctorMode = domain.DoctorModeExact
		state.Kind = flowWatchFacility
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.watch.facility"), cancelKeyboard(b.catalog, user.Locale))
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
	_, _ = b.api.Request(tgbotapi.NewCallback(cb.ID, ""))
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
			b.send(user, b.catalog.T(user.Locale, "flow.watch.interval"), intervalKeyboard())
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
	case strings.HasPrefix(data, "fac:"):
		b.handleFacility(ctx, user, data)
	case data == "doctor:any":
		state := b.getFlow(user.ID)
		if state == nil {
			return
		}
		state.Watch.DoctorMode = domain.DoctorModeAny
		state.Watch.FacilityMode = domain.FacilityModeAll
		state.Kind = flowWatchFacility
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.watch.facility"), cancelKeyboard(b.catalog, user.Locale))
	case data == "doctor:exact":
		state := b.getFlow(user.ID)
		if state == nil {
			return
		}
		state.Kind = flowWatchDoctor
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.watch.doctor_exact"), cancelKeyboard(b.catalog, user.Locale))
	case strings.HasPrefix(data, "interval:"):
		b.finishWatch(ctx, user, data)
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

func (b *Bot) finishFacilityStep(ctx context.Context, user domain.User, state *flow, text string) {
	if strings.EqualFold(text, "all") {
		state.Watch.FacilityMode = domain.FacilityModeAll
	} else {
		id, name, ok := parseIDName(text)
		if !ok {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.facility"), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		state.Watch.FacilityMode = domain.FacilityModeSelected
		state.Watch.FacilityIDs = []int{id}
		state.Watch.FacilityNames = []string{name}
	}
	b.setFlow(user.ID, state)
	b.send(user, b.catalog.T(user.Locale, "flow.watch.interval"), intervalKeyboard())
	_ = ctx
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
		title := b.catalog.T(user.Locale, "flow.watch.service") + "\n\nПоследние процедуры:"
		b.sendProcedureOptions(user, title, recent, procedureResultsKeyboard(recent, "", "", b.catalog, user.Locale))
		return
	}

	b.send(
		user,
		b.catalog.T(user.Locale, "flow.watch.service")+"\n\nНапишите часть названия процедуры, откройте популярные процедуры или обзор A-Z.",
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

	title := "Популярные процедуры LuxMed:"
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

func (b *Bot) showAllProcedures(ctx context.Context, user domain.User, state *flow) {
	b.showProcedureLetters(ctx, user, state)
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
		title := b.catalog.T(user.Locale, "flow.watch.facility") + "\n\nИзбранные клиники:"
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
		b.catalog.T(user.Locale, "flow.watch.facility")+"\n\nМожно выбрать все клиники в городе или открыть список и выбрать несколько.",
		facilityMenuKeyboard(nil, b.catalog, user.Locale),
	)
}

func (b *Bot) showFavoriteFacilities(ctx context.Context, user domain.User, state *flow, limit int) {
	_ = limit
	b.showFacilityPage(ctx, user, state, "favs", 0)
}

func (b *Bot) showAllFacilities(ctx context.Context, user domain.User, state *flow) {
	b.showFacilityPage(ctx, user, state, "all", 0)
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
		title = "Избранные клиники"
	default:
		mode = "all"
		facilities, err = b.db.FacilitiesPage(ctx, state.Watch.CityID, state.Watch.ServiceID, pageSize+1, offset)
		title = "Клиники"
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

	text := fmt.Sprintf(
		"%s. Показаны %d–%d.\n\nНажмите номера клиник, которые хотите выбрать. Потом нажмите “Готово”.",
		title,
		offset+1,
		offset+len(facilities),
	)

	if len(state.Watch.FacilityIDs) > 0 {
		text += fmt.Sprintf("\n\nВыбрано клиник: %d.", len(state.Watch.FacilityIDs))
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
		b.send(user, "Выберите хотя бы одну клинику или нажмите “Все клиники в городе”.", facilityMenuKeyboard(nil, b.catalog, user.Locale))
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

		lines = append(lines, fmt.Sprintf("%s %d. %s\n   ID: %d", prefix, i+1, label, facility.ID))
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

func (b *Bot) showTimeStep(user domain.User, state *flow) {
	state.Kind = flowWatchTime
	b.setFlow(user.ID, state)

	b.send(
		user,
		"Шаг 4/8: выберите время приёма.\n\nФильтр времени применяется на нашей стороне после получения слотов из LuxMed.",
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
		b.send(user, "Выберите время кнопкой или напишите диапазон, например: 09:00-13:00", timeWindowKeyboard(b.catalog, user.Locale))
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
		b.send(user, "Не понял время. Напишите диапазон в формате 09:00-13:00 или выберите кнопку.", timeWindowKeyboard(b.catalog, user.Locale))
		return
	}

	state.Watch.TimeWindows = []domain.TimeWindow{window}
	b.finishTimeStep(ctx, user, state)
}

func (b *Bot) finishTimeStep(ctx context.Context, user domain.User, state *flow) {
	b.setFlow(user.ID, state)
	b.send(user, b.catalog.T(user.Locale, "flow.watch.interval"), intervalKeyboard())
	_ = ctx
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

	b.send(user, "Выберите первую букву процедуры.", procedureLettersKeyboard(letters, b.catalog, user.Locale))
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

	title := fmt.Sprintf(
		"Процедуры на букву %s. Показаны %d–%d.\n\nНажмите номер под сообщением или напишите новый поисковый запрос.",
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

	title := fmt.Sprintf(
		"Результаты поиска по “%s”. Показаны %d–%d.\n\nНажмите номер под сообщением или напишите новый поисковый запрос.",
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
		lines = append(lines, fmt.Sprintf("%d. %s\n   ID: %d", i+1, proc.Name, proc.ID))
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
	switch parts[1] {
	case "pause":
		_ = b.db.SetWatchStatus(ctx, id, domain.WatchStatusPaused)
		b.send(user, b.catalog.T(user.Locale, "watch.paused"), mainKeyboard(b.catalog, user.Locale))
	case "resume":
		_ = b.db.SetWatchStatus(ctx, id, domain.WatchStatusActive)
		b.send(user, b.catalog.T(user.Locale, "watch.resumed"), mainKeyboard(b.catalog, user.Locale))
	case "delete":
		_ = b.db.SetWatchStatus(ctx, id, domain.WatchStatusDeleted)
		b.send(user, b.catalog.T(user.Locale, "watch.deleted"), mainKeyboard(b.catalog, user.Locale))
	case "check":
		if b.runner == nil {
			return
		}
		watch, err := b.db.Watch(ctx, id)
		if err == nil {
			go func() { _ = b.runner.CheckWatch(context.Background(), watch) }()
		}
	case "show":
		b.showWatches(ctx, user)
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
		count, _ := b.db.CountHistoryForWatch(ctx, watch.ID)
		last := "-"
		if watch.LastCheckedAt != nil {
			last = watch.LastCheckedAt.Format("2006-01-02 15:04")
		}
		text := b.catalog.T(user.Locale, "watch.list.item", watch.Name, watch.Status, watch.CityName, watch.ServiceName, last, count)
		b.send(user, text, watchKeyboard(b.catalog, user.Locale, watch))
	}
}

func (b *Bot) showHistory(ctx context.Context, user domain.User) {
	items, err := b.db.RecentHistory(ctx, user.ID, 10, 0)
	if err != nil || len(items) == 0 {
		b.send(user, b.catalog.T(user.Locale, "history.empty"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("%s\n%s\n%s\n%s", item.DateTime.Format("2006-01-02 15:04"), item.ServiceName, item.DoctorName, item.FacilityName))
	}
	b.send(user, strings.Join(lines, "\n\n"), mainKeyboard(b.catalog, user.Locale))
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
	b.send(user, b.catalog.T(user.Locale, "flow.invite.accepted"), mainKeyboard(b.catalog, user.Locale))
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
	b.send(user, "Invite code: "+code, nil)
}

func (b *Bot) send(user domain.User, text string, markup any) {
	msg := tgbotapi.NewMessage(user.TelegramChatID, text)
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
