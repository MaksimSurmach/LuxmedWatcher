// Command luxmed-mcp is a stdio MCP adapter that exposes a fixed set of
// LuxmedWatcher control-API routes to Claude Code. stdout carries MCP only.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultURL = "http://127.0.0.1:8094"
	maxReq     = 32 << 10
	maxResp    = 4 << 20
)

type core struct {
	base, key string
	hc        *http.Client
}

// parseBase accepts only http://<loopback>[:port] with nothing else.
func parseBase(raw string) (string, error) {
	if raw == "" {
		return defaultURL, nil
	}
	bad := errors.New("LUXMED_CONTROL_URL must be http://<loopback host>[:port] with no userinfo, path, query or fragment")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Path != "" || strings.ContainsAny(raw, "?#") {
		return "", bad
	}
	host := u.Hostname()
	if host == "localhost" {
		port := u.Port()
		if port == "" {
			port = "80"
		}
		return "http://" + net.JoinHostPort("127.0.0.1", port), nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return "", bad
	}
	return "http://" + u.Host, nil
}

func newCore(rawURL, key string) (*core, error) {
	base, err := parseBase(rawURL)
	if err != nil {
		return nil, err
	}
	if !luxmed.ValidTicketKey(key) {
		return nil, errors.New("LUXMED_CONTROL_KEY must be exactly 64 hex characters")
	}
	return &core{base, key, &http.Client{
		Timeout:       20 * time.Second,
		Transport:     &http.Transport{}, // no proxy
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// do performs one bounded request. Errors are deliberately generic: neither the
// token nor upstream error bodies are ever surfaced.
func (c *core) do(ctx context.Context, method, path string, in any) (any, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil || len(b) > maxReq {
			return nil, errors.New("request invalid or too large")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, errors.New("request invalid")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, errors.New("core request failed (is the Go core running?)")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResp+1))
	if err != nil || len(b) > maxResp {
		return nil, errors.New("core response unreadable or too large")
	}
	if resp.StatusCode == http.StatusConflict {
		return nil, errors.New("reconnect Lux Med in the Telegram Mini App and try again")
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("core returned HTTP %d", resp.StatusCode)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // keep int64 IDs exact
	if dec.Decode(&v) != nil {
		return nil, errors.New("core returned invalid JSON")
	}
	return v, nil
}

type out struct {
	Data any `json:"data"`
}

func reg[In any](s *mcp.Server, readOnly bool, name, desc string, f func(context.Context, In) (any, error)) {
	mcp.AddTool(s, &mcp.Tool{
		Name: name, Description: desc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, out, error) {
		d, err := f(ctx, in)
		return nil, out{d}, err
	})
}

type (
	none   struct{}
	procIn struct {
		CityID int    `json:"city_id" jsonschema:"City ID from list_cities."`
		Query  string `json:"query" jsonschema:"Free-text procedure/service name to search for. Use an empty string to browse."`
		Offset int    `json:"offset,omitempty" jsonschema:"Optional paging offset from a previous result."`
	}
	provIn struct {
		CityID    int `json:"city_id" jsonschema:"City ID from list_cities."`
		ServiceID int `json:"service_id" jsonschema:"Service ID from search_procedures."`
	}
	prefIn struct {
		Locale string `json:"locale" jsonschema:"One of pl, en, ru, be, uk."`
		CityID int    `json:"city_id" jsonschema:"Default city ID from list_cities, or 0 to clear it."`
	}
	window struct {
		Weekday int    `json:"weekday" jsonschema:"0 for any day, or 1 (Monday) to 7 (Sunday)."`
		From    string `json:"from" jsonschema:"Start time HH:MM."`
		To      string `json:"to" jsonschema:"End time HH:MM."`
	}
	searchIn struct {
		ID           int64    `json:"id,omitempty" jsonschema:"Existing search ID to update; omit to create a new active search."`
		CityID       int      `json:"city_id" jsonschema:"City ID from list_cities."`
		ServiceID    int      `json:"service_id" jsonschema:"Service ID from search_procedures."`
		DoctorID     int      `json:"doctor_id,omitempty" jsonschema:"Doctor ID from list_providers; omit for any doctor."`
		FacilityIDs  []int    `json:"facility_ids,omitempty" jsonschema:"Facility IDs from list_providers; omit for any facility."`
		DateFrom     string   `json:"date_from,omitempty" jsonschema:"Earliest date, ISO YYYY-MM-DD."`
		DateTo       string   `json:"date_to,omitempty" jsonschema:"Latest date, ISO YYYY-MM-DD."`
		NextDays     int      `json:"next_days,omitempty" jsonschema:"Rolling window of 1 to 90 days. Provide this or both date fields."`
		TimeWindows  []window `json:"time_windows,omitempty" jsonschema:"Allowed weekday/time windows; omit for any time."`
		CheckMinutes int      `json:"check_minutes" jsonschema:"How often to check, in minutes. Use the workspace minimum."`
	}
	statusIn struct {
		ID     int64  `json:"id" jsonschema:"Search ID from get_workspace."`
		Status string `json:"status" jsonschema:"Either active or paused."`
	}
	// Wire formats; keys match the core's documented HTTP bodies.
	searchBody struct {
		ID           int64    `json:"ID,omitempty"`
		CityID       int      `json:"CityID"`
		ServiceID    int      `json:"ServiceID"`
		DoctorID     int      `json:"DoctorID,omitempty"`
		FacilityIDs  []int    `json:"FacilityIDs,omitempty"`
		DateFrom     string   `json:"DateFrom,omitempty"`
		DateTo       string   `json:"DateTo,omitempty"`
		NextDays     int      `json:"NextDays,omitempty"`
		TimeWindows  []window `json:"TimeWindows,omitempty"`
		CheckMinutes int      `json:"CheckMinutes"`
	}
)

func register(s *mcp.Server, c *core) {
	get := func(path string) func(context.Context, none) (any, error) {
		return func(ctx context.Context, _ none) (any, error) { return c.do(ctx, "GET", path, nil) }
	}
	// Mutations answer 204; give the model an explicit confirmation.
	mutate := func(ctx context.Context, path string, body any) (any, error) {
		if _, err := c.do(ctx, "POST", path, body); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	}

	reg(s, true, "get_workspace", "Read the current workspace: settings, check intervals and all current searches with their IDs and filters. Call this first, and before any edit.", get("/v1/workspace"))
	reg(s, true, "list_cities", "List available cities as id/name pairs. Use these city IDs in every other tool; never guess IDs.", get("/v1/cities"))
	reg(s, true, "search_procedures", "Search procedures/services by name in a city to find service IDs. Requires a city ID from list_cities.",
		func(ctx context.Context, in procIn) (any, error) {
			return c.do(ctx, "POST", "/v1/procedures", map[string]any{"CityID": in.CityID, "Query": in.Query, "Offset": in.Offset})
		})
	reg(s, true, "list_providers", "List doctors and facilities offering a service in a city, to obtain doctor and facility IDs. Requires IDs from list_cities and search_procedures.",
		func(ctx context.Context, in provIn) (any, error) {
			return c.do(ctx, "POST", "/v1/providers", map[string]any{"CityID": in.CityID, "ServiceID": in.ServiceID})
		})
	reg(s, false, "set_preferences", "Set the UI locale and default city (city_id 0 clears the city). Both values are always written, so read get_workspace first and resend the unchanged one.",
		func(ctx context.Context, in prefIn) (any, error) {
			if !slices.Contains([]string{"pl", "en", "ru", "be", "uk"}, in.Locale) {
				return nil, errors.New("locale must be one of pl, en, ru, be, uk")
			}
			return mutate(ctx, "/v1/preferences", map[string]any{"Locale": in.Locale, "CityID": in.CityID})
		})
	reg(s, false, "save_search", "Create or update a watched appointment search. Fetch IDs first via get_workspace, list_cities, search_procedures and list_providers. Without id a new active search is created. With id the search is FULLY REPLACED: optional filters you omit are cleared, so when editing copy every existing field from get_workspace and change only what the user asked. This tool cannot book appointments or delete searches.",
		func(ctx context.Context, in searchIn) (any, error) {
			return mutate(ctx, "/v1/searches", searchBody(in))
		})
	reg(s, false, "set_search_status", "Pause or resume an existing search by ID (from get_workspace). Other fields are untouched.",
		func(ctx context.Context, in statusIn) (any, error) {
			if in.Status != "active" && in.Status != "paused" {
				return nil, errors.New("status must be active or paused")
			}
			return mutate(ctx, "/v1/searches/status", map[string]any{"ID": in.ID, "Status": in.Status})
		})
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	c, err := newCore(os.Getenv("LUXMED_CONTROL_URL"), os.Getenv("LUXMED_CONTROL_KEY"))
	if err != nil {
		log.Fatal("luxmed-mcp: ", err)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "luxmed-mcp", Version: "0.1.0"}, nil)
	register(s, c)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Print("luxmed-mcp: server stopped: ", err)
		os.Exit(1)
	}
}
