package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseBase(t *testing.T) {
	for raw, ok := range map[string]bool{
		"": true, "http://127.0.0.1:9": true, "http://localhost:8094": true, "http://[::1]:8094": true,
		"https://127.0.0.1": false, "http://example.com": false, "http://10.0.0.1": false,
		"http://u:p@127.0.0.1": false, "http://127.0.0.1/x": false, "http://127.0.0.1/": false,
		"http://127.0.0.1?a=1": false, "http://127.0.0.1#f": false,
	} {
		if _, err := parseBase(raw); (err == nil) != ok {
			t.Errorf("parseBase(%q) err=%v, want ok=%v", raw, err, ok)
		}
	}
}

func TestKeyAndDo(t *testing.T) {
	key := strings.Repeat("ab", 32)
	if _, err := newCore("", key[:63]); err == nil {
		t.Error("short key accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cities":
			if r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`[{"id":9007199254740993}]`))
		case "/v1/searches":
			w.WriteHeader(204)
		default:
			http.Redirect(w, r, "/v1/cities", http.StatusFound)
		}
	}))
	defer srv.Close()
	c, err := newCore(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if v, err := c.do(ctx, "GET", "/v1/cities", nil); err != nil || v == nil {
		t.Fatalf("cities: %v %v", v, err)
	}
	if v, err := c.do(ctx, "POST", "/v1/searches", map[string]int{"a": 1}); err != nil || v != nil {
		t.Fatalf("204: %v %v", v, err)
	}
	if _, err := c.do(ctx, "GET", "/v1/workspace", nil); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("redirect not rejected cleanly: %v", err)
	}
}
