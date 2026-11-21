package miniapp

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

const dashboardPrefix = "/api/dashboard/"

type DashboardConfig struct {
	Origin          string
	BotID, OwnerID  int64
	MinimumInterval time.Duration
	DefaultInterval time.Duration
}

type dashboard struct {
	cfg     DashboardConfig
	key     ed25519.PublicKey
	db      *storage.DB
	client  luxmed.Client
	check   func(context.Context) error
	connect func(context.Context) (string, error)
}

func Dashboard(cfg DashboardConfig, db *storage.DB, browser *luxmed.Browser, app *luxmed.MiniApp) http.Handler {
	d := &dashboard{cfg: cfg, key: productionKey(), db: db, client: browser.Client(), check: browser.Check, connect: app.Link}
	return d.handler()
}

func (d *dashboard) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+dashboardPrefix+"load", d.authorize(d.load))
	mux.HandleFunc("POST "+dashboardPrefix+"history", d.authorize(d.history))
	mux.HandleFunc("POST "+dashboardPrefix+"cities", d.authorize(d.cities))
	mux.HandleFunc("POST "+dashboardPrefix+"procedures", d.authorize(d.procedures))
	mux.HandleFunc("POST "+dashboardPrefix+"providers", d.authorize(d.providers))
	mux.HandleFunc("POST "+dashboardPrefix+"settings", d.authorize(d.settings))
	mux.HandleFunc("POST "+dashboardPrefix+"watch", d.authorize(d.saveWatch))
	mux.HandleFunc("POST "+dashboardPrefix+"status", d.authorize(d.status))
	mux.HandleFunc("POST "+dashboardPrefix+"account", d.authorize(d.account))
	mux.HandleFunc("POST "+dashboardPrefix+"connect", d.authorize(d.openBrowser))
	return mux
}

type dashboardAction func(http.ResponseWriter, *http.Request, domain.User)

func (d *dashboard) authorize(action dashboardAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != d.cfg.Origin || !authorizedUserWithMaxAge(
			r.Header.Get("X-Telegram-Init-Data"), d.cfg.BotID, d.cfg.OwnerID, time.Now(), time.Hour, d.key,
		) {
			reply(w, http.StatusUnauthorized, nil)
			return
		}
		user, err := d.db.UserByTelegramID(r.Context(), d.cfg.OwnerID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !user.IsActive() {
			reply(w, http.StatusForbidden, nil)
			return
		}
		if err != nil {
			reply(w, http.StatusServiceUnavailable, nil)
			return
		}
		action(w, r, user)
	}
}

type dashboardHistory struct {
	ID       int64
	WatchID  int64
	DateTime time.Time
	Service  string
	Doctor   string
	Facility string
	Address  string
	Status   domain.HistoryStatus
}

func (d *dashboard) load(w http.ResponseWriter, r *http.Request, user domain.User) {
	watches, err := d.db.WatchesByUser(r.Context(), user.ID)
	if err != nil {
		reply(w, http.StatusServiceUnavailable, nil)
		return
	}
	reply(w, http.StatusOK, struct {
		Watches        []domain.Watch
		Locale         string
		CityID         *int
		CityName       string
		MinimumMinutes int
		DefaultMinutes int
	}{watches, user.Locale, user.PreferredCityID, user.PreferredCityName,
		int((d.cfg.MinimumInterval + time.Minute - 1) / time.Minute),
		int((d.cfg.DefaultInterval + time.Minute - 1) / time.Minute)})
}

func (d *dashboard) history(w http.ResponseWriter, r *http.Request, user domain.User) {
	var input struct{ Offset int }
	if !decodeDashboard(w, r, &input) {
		return
	}
	if input.Offset < 0 || input.Offset > 10_000 {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	const pageSize = 24
	items, err := d.db.RecentHistory(r.Context(), user.ID, pageSize+1, input.Offset)
	if err != nil {
		reply(w, http.StatusServiceUnavailable, nil)
		return
	}
	slots := make([]dashboardHistory, 0, min(pageSize, len(items)))
	for _, item := range items[:min(pageSize, len(items))] {
		slots = append(slots, dashboardHistory{item.ID, item.WatchID, item.DateTime, item.ServiceName,
			item.DoctorName, item.FacilityName, item.Address, item.Status})
	}
	reply(w, http.StatusOK, struct {
		Slots []dashboardHistory
		More  bool
	}{slots, len(items) > pageSize})
}

func (d *dashboard) cities(w http.ResponseWriter, r *http.Request, _ domain.User) {
	ctx := r.Context()
	items, err := d.db.Cities(ctx, 300)
	if err == nil && len(items) == 0 {
		items, err = d.client.GetCities(ctx)
		if err == nil {
			err = d.db.UpsertCities(ctx, items)
		}
	}
	dashboardResult(w, items, err)
}

type catalogInput struct {
	CityID    int
	ServiceID int
	Query     string
	Offset    int
}

func (d *dashboard) procedures(w http.ResponseWriter, r *http.Request, _ domain.User) {
	var input catalogInput
	if !decodeDashboard(w, r, &input) {
		return
	}
	if utf8.RuneCountInString(input.Query) > 80 || input.Offset < 0 || input.Offset > 5000 {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	ctx := r.Context()
	city, err := d.db.City(ctx, input.CityID)
	if err == nil {
		var cached []domain.Procedure
		cached, err = d.db.SearchProceduresPage(ctx, city.ID, "", 1, 0)
		if err == nil && len(cached) == 0 {
			err = d.cacheProcedures(ctx, city)
		}
	}
	if err != nil {
		dashboardResult(w, nil, err)
		return
	}
	const pageSize = 24
	items, err := d.db.SearchProceduresPage(ctx, city.ID, input.Query, pageSize+1, input.Offset)
	dashboardResult(w, struct {
		Items []domain.Procedure
		More  bool
	}{items[:min(pageSize, len(items))], len(items) > pageSize}, err)
}

func (d *dashboard) cacheProcedures(ctx context.Context, city domain.City) error {
	items, err := d.client.GetServices(ctx)
	if err != nil {
		return err
	}
	procedures := make([]domain.Procedure, 0, len(items))
	for _, item := range items {
		procedures = append(procedures, domain.Procedure{ID: item.ID, Name: item.Name})
	}
	return d.db.UpsertProcedures(ctx, city, procedures)
}

func (d *dashboard) providers(w http.ResponseWriter, r *http.Request, _ domain.User) {
	var input catalogInput
	if !decodeDashboard(w, r, &input) {
		return
	}
	if _, err := d.db.Procedure(r.Context(), input.CityID, input.ServiceID); err != nil {
		dashboardResult(w, nil, err)
		return
	}
	items, err := d.client.GetDoctorsAndFacilities(r.Context(), input.CityID, input.ServiceID)
	dashboardResult(w, items, err)
}

func (d *dashboard) settings(w http.ResponseWriter, r *http.Request, user domain.User) {
	var input struct {
		Locale string
		CityID int
	}
	if !decodeDashboard(w, r, &input) {
		return
	}
	switch input.Locale {
	case "ru", "en", "pl":
	default:
		reply(w, http.StatusBadRequest, nil)
		return
	}
	var city domain.City
	var err error
	if input.CityID != 0 {
		city, err = d.db.City(r.Context(), input.CityID)
	}
	if err == nil {
		err = d.db.SetUserPreferences(r.Context(), user.ID, input.Locale, city)
	}
	dashboardResult(w, nil, err)
}

func (d *dashboard) status(w http.ResponseWriter, r *http.Request, user domain.User) {
	var input struct {
		ID     int64
		Status domain.WatchStatus
	}
	if !decodeDashboard(w, r, &input) {
		return
	}
	switch input.Status {
	case domain.WatchStatusActive, domain.WatchStatusPaused, domain.WatchStatusDeleted:
		dashboardResult(w, nil, d.db.SetUserWatchStatus(r.Context(), user.ID, input.ID, input.Status))
	default:
		reply(w, http.StatusBadRequest, nil)
	}
}

func (d *dashboard) account(w http.ResponseWriter, r *http.Request, _ domain.User) {
	err := d.check(r.Context())
	switch {
	case err == nil:
		reply(w, http.StatusOK, map[string]string{"state": "connected"})
	case errors.Is(err, luxmed.ErrAuthentication):
		reply(w, http.StatusOK, map[string]string{"state": "needs_login"})
	default:
		reply(w, http.StatusServiceUnavailable, nil)
	}
}

func (d *dashboard) openBrowser(w http.ResponseWriter, r *http.Request, _ domain.User) {
	if !authorizedUser(r.Header.Get("X-Telegram-Init-Data"), d.cfg.BotID, d.cfg.OwnerID, time.Now(), d.key) {
		reply(w, http.StatusUnauthorized, nil)
		return
	}
	link, err := d.connect(r.Context())
	dashboardResult(w, map[string]string{"url": link}, err)
}

func decodeDashboard(w http.ResponseWriter, r *http.Request, target any) bool {
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(target) != nil {
		reply(w, http.StatusBadRequest, nil)
		return false
	}
	return true
}

func dashboardResult(w http.ResponseWriter, value any, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		reply(w, http.StatusNotFound, nil)
	case errors.Is(err, luxmed.ErrAuthentication):
		reply(w, http.StatusConflict, map[string]string{"error": "needs_login"})
	case err != nil:
		reply(w, http.StatusServiceUnavailable, nil)
	default:
		if value == nil {
			reply(w, http.StatusNoContent, nil)
		} else {
			reply(w, http.StatusOK, value)
		}
	}
}

type watchInput struct {
	ID           int64
	CityID       int
	ServiceID    int
	DoctorID     int
	FacilityIDs  []int
	DateFrom     string
	DateTo       string
	NextDays     int
	TimeWindows  []domain.TimeWindow
	CheckMinutes int
}

func (d *dashboard) saveWatch(w http.ResponseWriter, r *http.Request, user domain.User) {
	var input watchInput
	if !decodeDashboard(w, r, &input) {
		return
	}
	watch, ok := input.schedule(d.cfg.MinimumInterval, time.Now())
	if !ok {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	ctx := r.Context()
	var previous domain.Watch
	if input.ID != 0 {
		var err error
		previous, err = d.db.Watch(ctx, input.ID)
		if err != nil {
			dashboardResult(w, nil, err)
			return
		}
		if previous.UserID != user.ID || previous.Status == domain.WatchStatusDeleted {
			reply(w, http.StatusNotFound, nil)
			return
		}
	}
	city, err := d.db.City(ctx, input.CityID)
	if err != nil {
		dashboardResult(w, nil, err)
		return
	}
	service, err := d.db.Procedure(ctx, city.ID, input.ServiceID)
	if err != nil {
		dashboardResult(w, nil, err)
		return
	}
	watch.ID, watch.UserID, watch.Name = input.ID, user.ID, service.Name+" · "+city.Name
	watch.CityID, watch.CityName = city.ID, city.Name
	watch.ServiceID, watch.ServiceName = service.ID, service.Name
	watch.DoctorMode, watch.FacilityMode = domain.DoctorModeAny, domain.FacilityModeAll
	if input.DoctorID != 0 || len(input.FacilityIDs) != 0 {
		if previous.CityID == city.ID && previous.ServiceID == service.ID &&
			(previous.DoctorID == nil && input.DoctorID == 0 || previous.DoctorID != nil && *previous.DoctorID == input.DoctorID) &&
			sameFacilitySelection(previous.FacilityIDs, input.FacilityIDs) {
			watch.DoctorID, watch.DoctorName, watch.DoctorMode = previous.DoctorID, previous.DoctorName, previous.DoctorMode
			watch.FacilityIDs, watch.FacilityNames, watch.FacilityMode = previous.FacilityIDs, previous.FacilityNames, previous.FacilityMode
		} else {
			data, err := d.client.GetDoctorsAndFacilities(ctx, city.ID, service.ID)
			if err != nil {
				dashboardResult(w, nil, err)
				return
			}
			if !input.applyProviders(&watch, data) {
				reply(w, http.StatusBadRequest, nil)
				return
			}
		}
	}
	if input.ID == 0 {
		_, err = d.db.CreateWatch(ctx, watch)
	} else {
		err = d.db.UpdateWatch(ctx, watch)
	}
	dashboardResult(w, nil, err)
}

func sameFacilitySelection(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for _, id := range a {
		found := false
		for _, candidate := range b {
			found = found || candidate == id
		}
		if !found {
			return false
		}
	}
	return true
}

func (input watchInput) applyProviders(watch *domain.Watch, data domain.DoctorsAndFacilities) bool {
	if input.DoctorID != 0 {
		for _, doctor := range data.Doctors {
			if doctor.ID == input.DoctorID {
				watch.DoctorID, watch.DoctorName, watch.DoctorMode = &doctor.ID, doctor.DisplayName(), domain.DoctorModeExact
				break
			}
		}
		if watch.DoctorID == nil {
			return false
		}
	}
	for _, id := range input.FacilityIDs {
		for _, facility := range data.Facilities {
			if facility.ID == id {
				watch.FacilityIDs = append(watch.FacilityIDs, id)
				watch.FacilityNames = append(watch.FacilityNames, facility.Name)
				break
			}
		}
	}
	if len(watch.FacilityIDs) > 0 {
		watch.FacilityMode = domain.FacilityModeSelected
	}
	return len(watch.FacilityIDs) == len(input.FacilityIDs)
}

func (input watchInput) schedule(minimum time.Duration, now time.Time) (domain.Watch, bool) {
	var watch domain.Watch
	if input.ID < 0 || input.CityID < 1 || input.ServiceID < 1 || input.DoctorID < 0 ||
		input.CheckMinutes < 1 || input.CheckMinutes > 1440 || time.Duration(input.CheckMinutes)*time.Minute < minimum ||
		len(input.FacilityIDs) > 30 || len(input.TimeWindows) > 7 {
		return watch, false
	}
	seen := make(map[int]bool)
	for _, id := range input.FacilityIDs {
		if id < 1 || seen[id] {
			return watch, false
		}
		seen[id] = true
	}
	watch.CheckIntervalSeconds = input.CheckMinutes * 60
	if input.DateFrom != "" || input.DateTo != "" {
		loc := luxmed.Location()
		today := now.In(loc)
		today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
		from, firstErr := time.ParseInLocation(time.DateOnly, input.DateFrom, loc)
		to, lastErr := time.ParseInLocation(time.DateOnly, input.DateTo, loc)
		if firstErr != nil || lastErr != nil || to.Before(today) || to.Before(from) || to.After(today.AddDate(0, 0, 90)) {
			return watch, false
		}
		watch.DateFrom, watch.DateTo = &from, &to
	} else {
		if input.NextDays < 1 || input.NextDays > 90 {
			return watch, false
		}
		watch.NextDays = input.NextDays
	}
	days := [8]bool{}
	for _, window := range input.TimeWindows {
		_, firstErr := time.Parse("15:04", window.From)
		_, lastErr := time.Parse("15:04", window.To)
		if window.Weekday < 0 || window.Weekday > 7 || days[window.Weekday] ||
			window.Weekday == 0 && len(input.TimeWindows) > 1 || firstErr != nil || lastErr != nil ||
			len(window.From) != 5 || len(window.To) != 5 || window.From > window.To {
			return watch, false
		}
		days[window.Weekday] = true
	}
	watch.TimeWindows = input.TimeWindows
	return watch, true
}
