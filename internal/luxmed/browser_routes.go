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
	Shared bool   `json:"-"`
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
		host := strings.ToLower(api.Hostname())
		if api.Port() == viewer.Port() || used[host] {
			return nil, invalid
		}
		used[host] = true
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

// SharedBrowserRoutes maps each configured account to its own scope at a shared worker.
func SharedBrowserRoutes(api, viewer, accountIDs string) (map[int64]BrowserRoute, error) {
	if workerEndpoint(api, "") == nil || workerEndpoint(viewer, "/vnc") == nil {
		return nil, errors.New("invalid shared browser worker endpoints")
	}
	routes := make(map[int64]BrowserRoute)
	for value := range strings.SplitSeq(accountIDs, ",") {
		value = strings.TrimSpace(value)
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 || id > 1<<53-1 || value != strconv.FormatInt(id, 10) {
			return nil, errors.New("invalid shared browser account ID")
		}
		if _, duplicate := routes[id]; duplicate {
			return nil, errors.New("duplicate shared browser account ID")
		}
		scope := "/accounts/" + value
		routes[id] = BrowserRoute{
			API: api + scope, Viewer: strings.TrimSuffix(viewer, "/vnc") + scope + "/vnc", Shared: true,
		}
	}
	return routes, nil
}
