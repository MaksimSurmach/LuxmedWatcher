package miniapp

import (
	"io/fs"
	"net/http"
	"strings"
)

func (a *app) public(web, vendor fs.FS) http.Handler {
	mux := http.NewServeMux()
	for _, file := range []string{"index.html", "app.js", "style.css"} {
		path := "/" + file
		if file == "index.html" {
			path = "/{$}"
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, web, file)
		})
	}
	for _, folder := range []string{"core", "vendor"} {
		mux.Handle("GET /vendor/"+folder+"/", http.StripPrefix("/vendor/", http.FileServerFS(vendor)))
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
