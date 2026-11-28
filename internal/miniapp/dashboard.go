package miniapp

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/procedures"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

const dashboardPrefix = "/api/dashboard/"

type DashboardConfig struct {
	Origin          string
	BotID           int64
	MinimumInterval time.Duration
	DefaultInterval time.Duration
	SyncAppMenu     func(context.Context, int64)
}

type dashboard struct {
	cfg          DashboardConfig
	key          ed25519.PublicKey
	sessions     sessions
	historyViews historyViews
	db           *storage.DB
	browsers     map[int64]*luxmed.Browser
	connect      func(context.Context, int64) (string, error)
	runner       *scheduler.Runner
	checkAccount func(context.Context, domain.User) (scheduler.BrowserSessionCheck, error)
	saveLogin    func(context.Context, domain.User, luxmed.Credentials, string) error
}

func Dashboard(
	cfg DashboardConfig,
	db *storage.DB,
	browsers map[int64]*luxmed.Browser,
	app *luxmed.MiniApp,
	runner *scheduler.Runner,
) http.Handler {
	d := &dashboard{
		cfg: cfg, key: productionKey(), sessions: newSessions(), db: db, browsers: browsers,
		connect: app.Link, runner: runner, saveLogin: runner.SaveCredentialsForAttempt, checkAccount: runner.CheckBrowserSession,
	}
	return d.handler()
}

func (d *dashboard) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+dashboardPrefix+"auth", d.authorizeTelegram(func(w http.ResponseWriter, _ *http.Request, user domain.User) {
		reply(w, http.StatusOK, map[string]string{"token": d.sessions.issue(user.TelegramUserID, time.Now())})
	}))
	mux.HandleFunc("POST "+dashboardPrefix+"load", d.authorize(d.load))
	mux.HandleFunc("POST "+dashboardPrefix+"history", d.authorize(d.history))
	mux.HandleFunc("POST "+dashboardPrefix+"cities", d.authorize(d.cities))
	mux.HandleFunc("POST "+dashboardPrefix+"procedures", d.authorize(d.procedures))
	mux.HandleFunc("POST "+dashboardPrefix+"procedure-picks", d.authorize(d.procedurePicks))
	mux.HandleFunc("POST "+dashboardPrefix+"providers", d.authorize(d.providers))
	mux.HandleFunc("POST "+dashboardPrefix+"settings", d.authorize(d.settings))
	mux.HandleFunc("POST "+dashboardPrefix+"watch", d.authorize(d.saveWatch))
	mux.HandleFunc("POST "+dashboardPrefix+"status", d.authorize(d.status))
	mux.HandleFunc("POST "+dashboardPrefix+"credentials", dashboardDeadline(d.authorize(d.saveCredentials)))
	mux.HandleFunc("POST "+dashboardPrefix+"forget-credentials", d.authorize(d.forgetCredentials))
	mux.HandleFunc("POST "+dashboardPrefix+"account", dashboardDeadline(d.authorize(d.account)))
	mux.HandleFunc("POST "+dashboardPrefix+"connect", d.browserLogin)
	mux.HandleFunc("POST "+dashboardPrefix+"resume-viewer", d.authorize(d.resumeViewer))
	mux.HandleFunc("POST "+dashboardPrefix+"viewer-access", func(w http.ResponseWriter, r *http.Request) {
		access := func(w http.ResponseWriter, _ *http.Request, _ domain.User) { reply(w, http.StatusNoContent, nil) }
		if r.Header.Get("Authorization") == "" {
			d.authorizeTelegram(access)(w, r)
			return
		}
		d.authorize(func(w http.ResponseWriter, r *http.Request, u domain.User) {
			if !authorizedUserWithMaxAge(r.Header.Get("X-Telegram-Init-Data"), d.cfg.BotID, u.TelegramUserID, time.Now(), dashboardSessionLifetime+5*time.Minute, d.key) {
				reply(w, http.StatusUnauthorized, nil)
				return
			}
			access(w, r, u)
		})(w, r)
	})
	return mux
}

// Leave room in the gateway's write deadline for the response.
func dashboardDeadline(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 17*time.Second)
		defer cancel()
		next(w, r.WithContext(ctx))
	}
}

type dashboardAction func(http.ResponseWriter, *http.Request, domain.User)

func (d *dashboard) authorize(action dashboardAction) http.HandlerFunc {
	return d.withUser(action, func(r *http.Request) (int64, bool) {
		return d.sessions.verify(r.Header.Get("Authorization"), time.Now())
	})
}

func (d *dashboard) authorizeTelegram(action dashboardAction) http.HandlerFunc {
	return d.withUser(action, func(r *http.Request) (int64, bool) {
		return verifiedTelegramID(r.Header.Get("X-Telegram-Init-Data"), d.cfg.BotID, time.Now(), 5*time.Minute, d.key)
	})
}

func (d *dashboard) withUser(action dashboardAction, identity func(*http.Request) (int64, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != d.cfg.Origin {
			reply(w, http.StatusUnauthorized, nil)
			return
		}
		id, ok := identity(r)
		if !ok {
			reply(w, http.StatusUnauthorized, nil)
			return
		}
		if d.browsers[id] == nil {
			reply(w, http.StatusForbidden, nil)
			return
		}
		user, err := d.db.UserByTelegramID(r.Context(), id)
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

type dashboardHistoryCursor struct {
	View   string
	Offset int
}

func (d *dashboard) load(w http.ResponseWriter, r *http.Request, user domain.User) {
	watches, access, err := d.db.WatchesWithReconnectState(r.Context(), user.ID)
	if err != nil {
		reply(w, http.StatusServiceUnavailable, nil)
		return
	}
	available, err := d.db.AvailableHistoryCount(r.Context(), user.ID, time.Now())
	if err != nil {
		reply(w, http.StatusServiceUnavailable, nil)
		return
	}
	reply(w, http.StatusOK, struct {
		SavedLogin             bool
		HasCredentials         bool
		CredentialRevision     int64
		CredentialSaveRevision int64
		Watches                []domain.Watch
		AvailableSlots         int
		Locale                 string
		CityID                 *int
		CityName               string
		MinimumMinutes         int
		DefaultMinutes         int
		ReconnectRequired      bool
		ReconnectEpisodeID     string `json:",omitempty"`
		ConfirmedUnix          *int64
		AccountStatus          storage.LuxMedAccessStatus
	}{access.SavedLogin, access.HasCredentials, access.CredentialRevision, access.CredentialSaveRevision, watches, available, user.Locale, user.PreferredCityID, user.PreferredCityName,
		int((d.cfg.MinimumInterval + time.Minute - 1) / time.Minute),
		int((d.cfg.DefaultInterval + time.Minute - 1) / time.Minute), access.EpisodeID != "", access.EpisodeID, access.ConfirmedUnix, access.Status})
}

func (d *dashboard) history(w http.ResponseWriter, r *http.Request, user domain.User) {
	const (
		pageSize      = 24
		maxPageOffset = 10_000
	)
	var input struct {
		Offset int
		After  *dashboardHistoryCursor
	}
	if !decodeDashboard(w, r, &input) {
		return
	}
	if input.Offset < 0 || input.Offset > maxPageOffset {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	now := time.Now()
	var ids []int64
	view := ""
	offset := input.Offset
	if input.After != nil {
		if input.Offset != 0 || input.After.View == "" || input.After.Offset <= 0 {
			reply(w, http.StatusBadRequest, nil)
			return
		}
		view, offset = input.After.View, input.After.Offset
		var ok bool
		ids, ok = d.historyViews.load(view, user.ID, now)
		if !ok || offset >= len(ids) {
			reply(w, http.StatusNotFound, nil)
			return
		}
	} else {
		var err error
		ids, err = d.db.UpcomingHistoryIDs(r.Context(), user.ID, now, maxPageOffset+pageSize)
		if err != nil {
			reply(w, http.StatusServiceUnavailable, nil)
			return
		}
	}
	slots := make([]dashboardHistory, 0, pageSize)
	for len(slots) < pageSize && offset < len(ids) {
		end := min(offset+pageSize-len(slots), len(ids))
		items, err := d.db.HistoryByIDs(r.Context(), user.ID, ids[offset:end])
		if err != nil {
			reply(w, http.StatusServiceUnavailable, nil)
			return
		}
		for _, item := range items {
			slots = append(slots, dashboardHistory{item.ID, item.WatchID, item.DateTime, item.ServiceName,
				item.DoctorName, item.FacilityName, item.Address, item.Status})
		}
		offset = end
	}
	more := offset < len(ids)
	var next *dashboardHistoryCursor
	if more {
		if view == "" {
			view = d.historyViews.save(user.ID, now, ids)
		}
		next = &dashboardHistoryCursor{View: view, Offset: offset}
	}
	reply(w, http.StatusOK, struct {
		Slots []dashboardHistory
		More  bool
		Next  *dashboardHistoryCursor `json:",omitempty"`
	}{slots, more, next})
}

func (d *dashboard) cities(w http.ResponseWriter, r *http.Request, user domain.User) {
	ctx := r.Context()
	var input struct{ Refresh bool }
	if !decodeDashboard(w, r, &input) {
		return
	}
	cached, err := d.db.Cities(ctx, 0)
	if err != nil || (!input.Refresh && len(cached) > 0) {
		dashboardResult(w, cached, err)
		return
	}
	err = d.runner.WithLuxMedClient(ctx, user, func(client luxmed.Client) error {
		items, err := client.GetCities(ctx)
		if err != nil {
			return err
		}
		return d.db.ReplaceCityCatalog(ctx, items)
	})
	if err != nil {
		if len(cached) > 0 {
			reply(w, http.StatusOK, cached)
		} else {
			dashboardResult(w, nil, err)
		}
		return
	}
	items, err := d.db.Cities(ctx, 0)
	dashboardResult(w, items, err)
}

type catalogInput struct {
	CityID    int
	ServiceID int
	Query     string
	Offset    int
}

func (d *dashboard) procedures(w http.ResponseWriter, r *http.Request, user domain.User) {
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
		err = d.ensureProcedureCatalog(ctx, user, city)
	}
	if err != nil {
		dashboardResult(w, nil, err)
		return
	}
	const pageSize = 24
	items, err := procedures.Search(ctx, d.db, city.ID, user.Locale, input.Query, pageSize+1, input.Offset)
	dashboardResult(w, struct {
		Items []domain.Procedure
		More  bool
	}{items[:min(pageSize, len(items))], len(items) > pageSize}, err)
}

type procedurePicks struct {
	PortalIncomplete bool `json:",omitempty"`
	Referred         []domain.Service
	Recent           []domain.Service
	Saved            []domain.Service
	Popular          []domain.Service
}

func (d *dashboard) procedurePicks(w http.ResponseWriter, r *http.Request, user domain.User) {
	var input struct{ CityID int }
	if !decodeDashboard(w, r, &input) {
		return
	}
	ctx := r.Context()
	city, err := d.db.City(ctx, input.CityID)
	if err != nil {
		dashboardResult(w, nil, err)
		return
	}
	portalCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	watches, err := d.db.WatchesByUser(ctx, user.ID)
	if err != nil {
		dashboardResult(w, nil, err)
		return
	}

	var saved []domain.Procedure
	for _, watch := range watches {
		if watch.CityID == city.ID && watch.ServiceID > 0 {
			saved = append(saved, domain.Procedure{ID: watch.ServiceID})
		}
	}

	portalErr := d.ensureProcedureCatalog(portalCtx, user, city)
	var referred, recent, popular []domain.Procedure
	if !stopRecommendationRequests(portalErr) {
		err := d.runner.WithLuxMedClient(portalCtx, user, func(client luxmed.Client) error {
			var err, result error
			if referrals, ok := client.(interface {
				GetReferredProcedures(context.Context) ([]domain.Procedure, error)
			}); ok {
				referred, err = referrals.GetReferredProcedures(portalCtx)
				result = errors.Join(result, err)
				if stopRecommendationRequests(err) {
					return result
				}
			}
			recent, err = client.GetRecentProcedures(portalCtx)
			result = errors.Join(result, err)
			if stopRecommendationRequests(err) {
				return result
			}
			popular, err = client.GetPopularProcedures(portalCtx)
			return errors.Join(result, err)
		})
		portalErr = errors.Join(portalErr, err)
	}
	picks := procedurePicks{PortalIncomplete: portalErr != nil}
	seen := make(map[int]bool)
	groups := []struct {
		target     *[]domain.Service
		candidates []domain.Procedure
	}{
		{&picks.Referred, referred},
		{&picks.Recent, recent},
		{&picks.Saved, saved},
		{&picks.Popular, popular},
	}
	for _, group := range groups {
		isSaved := group.target == &picks.Saved
		if isSaved && len(picks.Recent) > 0 && !picks.PortalIncomplete {
			continue
		}
		const limit = 4
		for _, item := range group.candidates {
			if seen[item.ID] {
				continue
			}
			var service domain.Procedure
			var err error
			if isSaved {
				service, err = d.serviceForUser(ctx, user.ID, city.ID, item.ID)
			} else {
				service, err = d.db.Procedure(ctx, city.ID, item.ID)
			}
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				dashboardResult(w, nil, err)
				return
			}
			seen[item.ID] = true
			*group.target = append(*group.target, domain.Service{ID: item.ID, Name: service.Name})
			if len(*group.target) == limit {
				break
			}
		}
	}
	reply(w, http.StatusOK, picks)
}

func stopRecommendationRequests(err error) bool {
	return errors.Is(err, luxmed.ErrAuthentication) || luxmed.RetryAfter(err) > 0 ||
		errors.Is(err, luxmed.ErrBrowserBusy) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)
}

func (d *dashboard) ensureProcedureCatalog(ctx context.Context, user domain.User, city domain.City) error {
	cached, err := d.db.SearchProceduresPage(ctx, city.ID, "", 1, 0)
	if err != nil || len(cached) > 0 {
		return err
	}
	var items []domain.Service
	err = d.runner.WithLuxMedClient(ctx, user, func(client luxmed.Client) error {
		var err error
		items, err = client.GetServices(ctx)
		return err
	})
	if err != nil {
		return err
	}
	procedures := make([]domain.Procedure, 0, len(items))
	for _, item := range items {
		procedures = append(procedures, domain.Procedure{ID: item.ID, Name: item.Name})
	}
	return d.db.UpsertProcedures(ctx, city, procedures)
}

func (d *dashboard) serviceForUser(ctx context.Context, userID int64, cityID, serviceID int) (domain.Procedure, error) {
	service, err := d.db.Procedure(ctx, cityID, serviceID)
	if !errors.Is(err, sql.ErrNoRows) {
		return service, err
	}
	name, err := d.db.SavedWatchServiceName(ctx, userID, cityID, serviceID)
	return domain.Procedure{ID: serviceID, CityID: cityID, Name: name}, err
}

func (d *dashboard) providers(w http.ResponseWriter, r *http.Request, user domain.User) {
	var input catalogInput
	if !decodeDashboard(w, r, &input) {
		return
	}
	if _, err := d.serviceForUser(r.Context(), user.ID, input.CityID, input.ServiceID); err != nil {
		dashboardResult(w, nil, err)
		return
	}
	items, err := d.providersForUser(r.Context(), user, input.CityID, input.ServiceID)
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
	if !slices.Contains(i18n.SupportedLocales, input.Locale) {
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
	if err == nil && d.cfg.SyncAppMenu != nil {
		d.cfg.SyncAppMenu(r.Context(), user.ID)
	}
	dashboardResult(w, nil, err)
}

func (d *dashboard) status(w http.ResponseWriter, r *http.Request, user domain.User) {
	d.setStatus(w, r, user, true)
}

func (d *dashboard) setStatus(w http.ResponseWriter, r *http.Request, user domain.User, allowDelete bool) {
	var input struct {
		ID     int64
		Status domain.WatchStatus
	}
	if !decodeDashboard(w, r, &input) {
		return
	}
	if input.Status != domain.WatchStatusActive && input.Status != domain.WatchStatusPaused &&
		!(allowDelete && input.Status == domain.WatchStatusDeleted) {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	dashboardResult(w, nil, d.db.SetUserWatchStatus(r.Context(), user.ID, input.ID, input.Status))
}

func (d *dashboard) providersForUser(ctx context.Context, user domain.User, cityID, serviceID int) (domain.DoctorsAndFacilities, error) {
	var providers domain.DoctorsAndFacilities
	err := d.runner.WithLuxMedClient(ctx, user, func(client luxmed.Client) error {
		var err error
		providers, err = client.GetDoctorsAndFacilities(ctx, cityID, serviceID)
		return err
	})
	return providers, err
}

func (d *dashboard) saveCredentials(w http.ResponseWriter, r *http.Request, user domain.User) {
	var input struct {
		luxmed.Credentials
		CredentialRevision     *int64
		CredentialSaveRevision *int64
	}
	if !decodeDashboard(w, r, &input) {
		return
	}
	creds := input.Credentials
	creds.Login = strings.TrimSpace(creds.Login)
	if creds.Login == "" || len(creds.Login) > 254 || creds.Password == "" || len(creds.Password) > 1024 {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	id, ok := credentialAttemptID(r)
	if !ok {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	if input.CredentialRevision == nil || *input.CredentialRevision != user.CredentialRevision ||
		input.CredentialSaveRevision == nil || *input.CredentialSaveRevision != user.CredentialSaveRevision {
		dashboardResult(w, nil, storage.ErrCredentialsChanged)
		return
	}
	dashboardResult(w, nil, d.saveLogin(r.Context(), user, creds, id))
}

func (d *dashboard) account(w http.ResponseWriter, r *http.Request, user domain.User) {
	check, err := d.checkAccount(r.Context(), user)
	result := struct {
		State              string `json:"state"`
		ReconnectRequired  bool
		ReconnectEpisodeID string `json:",omitempty"`
	}{
		State: "connected", ReconnectRequired: check.VerificationPending || check.EpisodeID != "",
		ReconnectEpisodeID: check.EpisodeID,
	}

	switch {
	case err == nil:
		if check.NoSavedSearches {
			result.State = string(storage.LuxMedAccessNoSearches)
		} else if check.VerificationPending {
			result.State = "verification_pending"
		}
	case errors.Is(err, luxmed.ErrUnavailable):
		reply(w, http.StatusServiceUnavailable, nil)
		return
	case errors.Is(err, luxmed.ErrAuthentication):
		result.State = "needs_login"
	case errors.Is(err, luxmed.ErrBrowserBusy):
		reply(w, http.StatusTooManyRequests, nil)
		return
	default:
		reply(w, http.StatusServiceUnavailable, nil)
		return
	}
	reply(w, http.StatusOK, result)
}

func credentialAttemptID(r *http.Request) (string, bool) {
	id := r.Header.Get("X-LuxMed-Login-Attempt")
	if len(id) > 64 {
		return "", false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return "", false
		}
	}
	return id, true
}

func dashboardCredentialRevisions(w http.ResponseWriter, r *http.Request) (domain.CredentialRevisions, bool) {
	var input struct {
		CredentialRevision     *int64
		CredentialSaveRevision *int64
	}
	if !decodeDashboard(w, r, &input) {
		return domain.CredentialRevisions{}, false
	}
	if input.CredentialRevision == nil || *input.CredentialRevision < 0 ||
		input.CredentialSaveRevision == nil || *input.CredentialSaveRevision < 0 {
		dashboardResult(w, nil, storage.ErrCredentialsChanged)
		return domain.CredentialRevisions{}, false
	}
	return domain.CredentialRevisions{
		CredentialRevision: *input.CredentialRevision, CredentialSaveRevision: *input.CredentialSaveRevision,
	}, true
}

func (d *dashboard) forgetCredentials(w http.ResponseWriter, r *http.Request, user domain.User) {
	revisions, ok := dashboardCredentialRevisions(w, r)
	if !ok {
		return
	}
	token, ok := credentialAttemptID(r)
	if !ok {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	if token != "" {
		choice := d.db.SelectBrowserLogin(user.TelegramUserID, token, revisions, true)
		dashboardResult(w, nil, d.db.ChooseBrowserLogin(r.Context(), user, choice))
		return
	}
	if revisions != user.CredentialRevisions() {
		dashboardResult(w, nil, storage.ErrCredentialsChanged)
		return
	}
	dashboardResult(w, nil, d.db.ForgetCredentials(r.Context(), user))
}

func (d *dashboard) browserLogin(w http.ResponseWriter, r *http.Request) {
	id, ok := d.sessions.verify(r.Header.Get("Authorization"), time.Now())
	if !ok || r.Header.Get("Origin") != d.cfg.Origin ||
		!authorizedUserWithMaxAge(r.Header.Get("X-Telegram-Init-Data"), d.cfg.BotID, id, time.Now(), dashboardSessionLifetime+5*time.Minute, d.key) {
		reply(w, http.StatusUnauthorized, nil)
		return
	}
	token, ok := credentialAttemptID(r)
	if !ok {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	if d.browsers[id] == nil {
		reply(w, http.StatusForbidden, nil)
		return
	}
	revisions, ok := dashboardCredentialRevisions(w, r)
	if !ok {
		return
	}
	attempt := d.db.SelectBrowserLogin(id, token, revisions, false)
	d.withUser(func(w http.ResponseWriter, r *http.Request, user domain.User) {
		d.openBrowser(w, r, user, attempt)
	}, func(*http.Request) (int64, bool) { return id, true })(w, r)
}

func (d *dashboard) openBrowser(w http.ResponseWriter, r *http.Request, user domain.User, attempt *storage.ChromeChoice) {
	if err := d.db.ChooseBrowserLogin(r.Context(), user, attempt); err != nil {
		dashboardResult(w, nil, err)
		return
	}
	d.issueViewerLink(w, r, user)
}

// Reattach the transport without selecting a login mode or changing the credential revisions.
func (d *dashboard) resumeViewer(w http.ResponseWriter, r *http.Request, user domain.User) {
	if !authorizedUserWithMaxAge(r.Header.Get("X-Telegram-Init-Data"), d.cfg.BotID, user.TelegramUserID, time.Now(), dashboardSessionLifetime+5*time.Minute, d.key) {
		reply(w, http.StatusUnauthorized, nil)
		return
	}
	_, saved, _, err := d.db.BrowserLoginMode(r.Context(), user.ID)
	if err != nil {
		dashboardResult(w, nil, err)
		return
	}
	if saved {
		dashboardResult(w, nil, storage.ErrCredentialsChanged)
		return
	}
	d.issueViewerLink(w, r, user)
}

func (d *dashboard) issueViewerLink(w http.ResponseWriter, r *http.Request, user domain.User) {
	link, err := d.connect(r.Context(), user.TelegramUserID)
	if err == nil {
		link += "&lang=" + i18n.Normalize(user.Locale, "pl")
	}
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
	case errors.Is(err, storage.ErrCredentialsChanged):
		reply(w, http.StatusPreconditionFailed, nil)
	case errors.Is(err, sql.ErrNoRows):
		reply(w, http.StatusNotFound, nil)
	case errors.Is(err, luxmed.ErrUnavailable):
		reply(w, http.StatusServiceUnavailable, nil)
		return
	case errors.Is(err, luxmed.ErrAuthentication):
		reply(w, http.StatusConflict, map[string]string{"error": "needs_login"})
	case errors.Is(err, luxmed.ErrBrowserBusy):
		reply(w, http.StatusTooManyRequests, nil)
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
	service, err := d.serviceForUser(ctx, user.ID, city.ID, input.ServiceID)
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
			data, err := d.providersForUser(ctx, user, city.ID, service.ID)
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
