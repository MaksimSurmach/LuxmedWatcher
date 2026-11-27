package miniapp

import (
	"crypto/subtle"
	"net/http"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

// Control serves the private configuration API for one assigned Telegram user.
func Control(cfg DashboardConfig, db *storage.DB, browsers map[int64]*luxmed.Browser, userID int64, key string, runner *scheduler.Runner) http.Handler {
	cfg.Origin = ""
	d := &dashboard{cfg: cfg, db: db, browsers: browsers, runner: runner}
	expected := []byte("Bearer " + key)
	configured := luxmed.ValidTicketKey(key)
	mux := http.NewServeMux()
	handle := func(route string, action dashboardAction) {
		mux.HandleFunc(route, d.withUser(action, func(r *http.Request) (int64, bool) {
			valid := subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), expected) == 1
			return userID, configured && valid
		}))
	}
	handle("GET /v1/workspace", d.load)
	handle("GET /v1/cities", d.cities)
	handle("POST /v1/procedures", d.procedures)
	handle("POST /v1/providers", d.providers)
	handle("POST /v1/preferences", d.settings)
	handle("POST /v1/searches", d.saveWatch)
	handle("POST /v1/searches/status", func(w http.ResponseWriter, r *http.Request, user domain.User) {
		d.setStatus(w, r, user, false)
	})
	return mux
}
