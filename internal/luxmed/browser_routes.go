package luxmed

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// BrowserRoute points to both services of one dedicated Chromium worker.
type BrowserRoute struct {
	API    string `json:"api"`
	Viewer string `json:"viewer"`
}

func ParseBrowserRoutes(raw string) (map[int64]BrowserRoute, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	invalid := errors.New("LUXMED_BROWSER_ROUTES must map distinct Telegram IDs to private HTTP browser workers")
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	routes := make(map[int64]BrowserRoute)
	used := make(map[string]bool)
	for dec.More() {
		token, err := dec.Token()
		text, ok := token.(string)
		id, idErr := strconv.ParseInt(text, 10, 64)
		if err != nil || !ok || idErr != nil || id <= 0 || text != strconv.FormatInt(id, 10) {
			return nil, invalid
		}
		if _, duplicate := routes[id]; duplicate {
			return nil, invalid
		}
		var route BrowserRoute
		if err := dec.Decode(&route); err != nil {
			return nil, invalid
		}
		api, viewer := workerEndpoint(route.API, ""), workerEndpoint(route.Viewer, "/vnc")
		if api == nil || viewer == nil || !strings.EqualFold(api.Hostname(), viewer.Hostname()) {
			return nil, invalid
		}
		apiKey, viewerKey := strings.ToLower(api.Host), strings.ToLower(viewer.Host)
		if apiKey == viewerKey || used[apiKey] || used[viewerKey] {
			return nil, invalid
		}
		used[apiKey], used[viewerKey] = true, true
		routes[id] = route
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') || len(routes) == 0 {
		return nil, invalid
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, invalid
	}
	return routes, nil
}

func workerEndpoint(raw, path string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil ||
		u.Path != path || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Port() == "" || strings.HasSuffix(u.Hostname(), ".") {
		return nil
	}
	return u
}
