package miniapp

import (
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"

	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
)

func (a *app) public(web, vendor fs.FS) http.Handler {
	mux := http.NewServeMux()
	for _, file := range []string{"index.html", "app.js", "style.css", "dashboard.html", "dashboard.js", "dashboard.css", "language.js"} {
		path := "/" + file
		switch file {
		case "index.html":
			path = "/{$}"
		case "dashboard.html":
			path = "/app"
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, web, file)
		})
	}
	mux.HandleFunc("GET /i18n/{locale}", func(w http.ResponseWriter, r *http.Request) {
		locale := r.PathValue("locale")
		if !slices.Contains(i18n.SupportedLocales, locale) {
			http.NotFound(w, r)
			return
		}
		data, err := i18n.Web(locale)
		if err != nil {
			reply(w, http.StatusServiceUnavailable, nil)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(data)
	})
	for _, folder := range []string{"core", "vendor"} {
		mux.Handle("GET /vendor/"+folder+"/", http.StripPrefix("/vendor/", http.FileServerFS(vendor)))
	}
	if a.config.DashboardAPI != "" {
		upstream, _ := url.Parse(a.config.DashboardAPI)
		mux.Handle("POST "+dashboardPrefix, &httputil.ReverseProxy{
			Rewrite: func(request *httputil.ProxyRequest) {
				request.SetURL(upstream)
				request.Out.Header.Del("Cookie")
				request.Out.Header.Del("Authorization")
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				reply(w, http.StatusServiceUnavailable, nil)
			},
		})
	}
	mux.HandleFunc("POST /api/session", a.session)
	mux.HandleFunc("GET /vnc", a.viewer)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		socketOrigin := "wss:" + strings.TrimPrefix(a.config.Origin, "https:")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' https://telegram.org; style-src 'self' 'unsafe-inline'; connect-src 'self' "+socketOrigin+"; img-src 'self' data: blob:; base-uri 'none'; form-action 'none'; frame-ancestors 'self' https://web.telegram.org")
		mux.ServeHTTP(w, r)
	})
}
