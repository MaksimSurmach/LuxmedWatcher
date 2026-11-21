package luxmed

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBrowserUsesWorkerWithoutPasswordOrPortalTokens(t *testing.T) {
	var open atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization-token") != "" || r.Header.Get("Cookie") != "" {
			t.Error("browser client sent portal credentials")
		}
		switch r.URL.Path {
		case "/session":
			if !open.Load() {
				w.WriteHeader(http.StatusUnauthorized)
			}
		case CitiesURL:
			fmt.Fprint(w, `[{"id":123,"name":"Sample"}]`)
		case "/viewer":
			if r.Method != http.MethodPost {
				t.Errorf("viewer method = %s", r.Method)
			}
			fmt.Fprintf(w, `{"ticket":%q}`, strings.Repeat("a", 64))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	browser, err := NewBrowser(server.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	if err := browser.Check(t.Context()); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("logged-out session = %v", err)
	}
	open.Store(true)
	if err := browser.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	cities, err := browser.Client().GetCities(t.Context())
	if err != nil || len(cities) != 1 || cities[0].ID != 123 {
		t.Fatalf("cities = %v, error = %v", cities, err)
	}
	link, err := browser.ViewerLink(t.Context())
	if err != nil || link != "http://localhost:18769/connect#"+strings.Repeat("a", 64) {
		t.Fatalf("unexpected viewer link: %v", err)
	}
}
