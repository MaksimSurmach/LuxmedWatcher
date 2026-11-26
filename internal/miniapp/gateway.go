package miniapp

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
)

func (a *app) public(web, vendor fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", localizedPage(web, "index.html"))
	mux.HandleFunc("GET /app", localizedPage(web, "dashboard.html"))
	for _, file := range []string{
		"boot.js", "app.js", "style.css", "dashboard.js", "dashboard.css", "language.js", "screencast.js",
	} {
		mux.HandleFunc("GET /"+file, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, web, file)
		})
	}
	mux.HandleFunc("GET /i18n.js", serveDictionaries)
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
	upstream, _ := url.Parse(a.config.DashboardAPI)
	mux.Handle("POST "+dashboardPrefix, &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(upstream)
			request.Out.Header.Del("Cookie")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			reply(w, http.StatusServiceUnavailable, nil)
		},
	})
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

func serveDictionaries(w http.ResponseWriter, _ *http.Request) {
	dictionaries := make(map[string]json.RawMessage, len(i18n.SupportedLocales))
	for _, locale := range i18n.SupportedLocales {
		data, err := i18n.Web(locale)
		if err != nil {
			reply(w, http.StatusServiceUnavailable, nil)
			return
		}
		dictionaries[locale] = data
	}
	var source bytes.Buffer
	source.WriteString("export default ")
	if err := json.NewEncoder(&source).Encode(dictionaries); err != nil {
		reply(w, http.StatusServiceUnavailable, nil)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Write(source.Bytes())
}

func localizedPage(web fs.FS, file string) http.HandlerFunc {
	page, parseErr := template.ParseFS(web, file)
	return func(w http.ResponseWriter, r *http.Request) {
		if parseErr != nil {
			http.NotFound(w, r)
			return
		}
		locale := pageLocale(r)
		data, err := i18n.Web(locale)
		var labels map[string]string
		if err != nil || json.Unmarshal(data, &labels) != nil {
			reply(w, http.StatusServiceUnavailable, nil)
			return
		}
		var html bytes.Buffer
		if err := page.Execute(&html, struct {
			Locale, JavaScript, Loading, Unavailable, Retry string
		}{
			Locale:      locale,
			JavaScript:  labels["common.javascript_required"],
			Loading:     labels["dashboard.loading"],
			Unavailable: labels["error.unavailable"],
			Retry:       labels["common.retry"],
		}); err != nil {
			reply(w, http.StatusServiceUnavailable, nil)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html.Bytes())
	}
}

func pageLocale(r *http.Request) string {
	if hint := r.URL.Query().Get("lang"); slices.Contains(i18n.SupportedLocales, hint) {
		return hint
	}
	best, bestQuality := "pl", 0.0
	for _, choice := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, option, _ := strings.Cut(choice, ";")
		locale := i18n.Normalize(tag, "-")
		if locale == "-" {
			continue
		}
		quality := 1.0
		if weight, hasWeight := strings.CutPrefix(strings.TrimSpace(option), "q="); hasWeight {
			quality, _ = strconv.ParseFloat(weight, 64)
		}
		if quality > bestQuality && quality <= 1 {
			best, bestQuality = locale, quality
		}
	}
	return best
}
