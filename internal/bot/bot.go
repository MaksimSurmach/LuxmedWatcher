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
	flowWatchService  flowKind = "watch_service"
	flowWatchDoctor   flowKind = "watch_doctor"
	flowWatchFacility flowKind = "watch_facility"
)

type flow struct {
	Kind  flowKind
	Login string
	Watch domain.Watch
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
		id, name, ok := parseIDName(text)
		if !ok {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.city"), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		state.Watch.CityID = id
		state.Watch.CityName = name
		state.Kind = flowWatchService
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.watch.service"), cancelKeyboard(b.catalog, user.Locale))
	case flowWatchService:
		id, name, ok := parseIDName(text)
		if !ok {
			b.send(user, b.catalog.T(user.Locale, "flow.watch.service"), cancelKeyboard(b.catalog, user.Locale))
			return
		}
		state.Watch.ServiceID = id
		state.Watch.ServiceName = name
		state.Kind = flowWatchDoctor
		b.setFlow(user.ID, state)
		b.send(user, b.catalog.T(user.Locale, "flow.watch.doctor"), doctorModeKeyboard(b.catalog, user.Locale))
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
		b.finishFacilityStep(ctx, user, state, text)
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
	case data == "cancel":
		b.clearFlow(user.ID)
		b.showMenu(user)
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
	b.setFlow(user.ID, &flow{Kind: flowWatchCity, Watch: domain.Watch{UserID: user.ID, NextDays: 14, DoctorMode: domain.DoctorModeAny, FacilityMode: domain.FacilityModeAll}})
	b.send(user, b.catalog.T(user.Locale, "flow.watch.city"), cancelKeyboard(b.catalog, user.Locale))
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
