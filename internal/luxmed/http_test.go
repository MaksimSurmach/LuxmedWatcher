package luxmed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func TestAuthenticateThenSearchAppointments(t *testing.T) {
	today := startOfDay(time.Now())
	firstDay := today.AddDate(0, 0, 1)
	lastDay := firstDay.AddDate(0, 0, 15)
	firstEnd := firstDay.AddDate(0, 0, 13)
	secondStart := firstEnd.AddDate(0, 0, 1)
	doctorID := 42
	creds := Credentials{Login: "test-user", Password: "test-password"}
	var logins, searches atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LoginURL:
			if r.Method != http.MethodPost {
				t.Errorf("login method = %s", r.Method)
			}
			if r.Header.Get("authorization-token") != "" || r.Header.Get("Cookie") != "" {
				t.Error("login received stale session")
			}
			data, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(data), creds.Login) || !strings.Contains(string(data), creds.Password) {
				t.Error("login body missing credentials")
			}
			login := logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "session", Value: fmt.Sprint(login), Path: "/"})
			fmt.Fprintf(w, `{"succeded":true,"token":"t%d"}`, login)
		case XsrfTokenURL:
			fmt.Fprint(w, `{"token":"xsrf"}`)
		case TermsURL:
			auth := logins.Load()
			if r.Header.Get("authorization-token") != fmt.Sprintf("t%d", auth) || r.Header.Get("X-XSRF-TOKEN") != "xsrf" {
				t.Error("search missing session tokens")
			}
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value != fmt.Sprint(auth) {
				t.Error("search missing latest session cookie")
			}
			query := r.URL.Query()
			if query.Get("searchPlace.id") != "1" || query.Get("serviceVariantId") != "2" ||
				query.Get("doctorsIds") != "42" || !reflect.DeepEqual(query["facilitiesIds"], []string{"5", "6"}) {
				t.Errorf("incorrect search filters: %v", query)
			}
			switch searches.Add(1) {
			case 1:
				http.Error(w, "expired", http.StatusUnauthorized)
			case 2:
				http.Error(w, "temporary failure", http.StatusBadGateway)
			case 3:
				if query.Get("searchDateFrom") != firstDay.Format(dateFormat) || query.Get("searchDateTo") != firstEnd.Format(dateFormat) {
					t.Errorf("incorrect first date range: %v", query)
				}
				fmt.Fprintf(w, `{"termsForService":{"termsForDays":[{"terms":[
					{"dateTimeFrom":"%sT10:15:00","doctor":{"id":42,"firstName":"A","lastName":"Doctor"},"clinicId":5,"clinic":"Example clinic","clinicGroup":"Example Street"},
					{"dateTimeFrom":"%sT11:15:00","doctor":{"id":99},"clinicId":5},
					{"dateTimeFrom":"%sT12:15:00","doctor":{"id":42},"clinicId":9},
					{"dateTimeFrom":"%sT13:15:00","doctor":{"id":42},"clinicId":5}
				]}]}}`, firstDay.Format(dateFormat), firstDay.Format(dateFormat), firstDay.Format(dateFormat), secondStart.Format(dateFormat))
			case 4:
				if query.Get("searchDateFrom") != secondStart.Format(dateFormat) || query.Get("searchDateTo") != lastDay.Format(dateFormat) {
					t.Errorf("incorrect second date range: %v", query)
				}
				fmt.Fprintf(w, `{"success":true,"termsForService":{"termsForDays":[{"terms":[{"id":123,"dateTimeFrom":"%sT14:00:00","doctor":{"id":42},"clinicId":6}]}]}}`, lastDay.Format(dateFormat))
			default:
				t.Error("too many searches")
				w.WriteHeader(http.StatusBadRequest)
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newHTTPClient(server.URL, false)
	if err := client.Authenticate(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	result, err := client.SearchAppointments(context.Background(), domain.Watch{
		CityID: 1, ServiceID: 2, DoctorID: &doctorID, FacilityIDs: []int{5, 6},
		DateFrom: &firstDay, DateTo: &lastDay,
	})
	apps := result.Appointments
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || logins.Load() != 2 || searches.Load() != 4 {
		t.Fatalf("appointments = %d, logins = %d, searches = %d", len(apps), logins.Load(), searches.Load())
	}
	app := apps[0]
	if app.DateTime.Location() != Location() || app.DateTime.Hour() != 10 || app.DateTime.Minute() != 15 ||
		app.FacilityName != "Example clinic" || app.Address != "Example Street" ||
		app.ExternalID == "" || app.ExternalID != app.Fingerprint || app.RawPayload != "" {
		t.Fatalf("unexpected normalized appointment: %+v", app)
	}
	legacy := app
	legacy.DateTime = time.Date(firstDay.Year(), firstDay.Month(), firstDay.Day(), 10, 15, 0, 0, time.UTC)
	if Fingerprint(legacy) != app.Fingerprint || apps[1].ExternalID != "123" {
		t.Fatal("slot identity changed")
	}
}

func TestReadOnlyBackoffAndRedactedErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		http.Error(w, "private login and medical data", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := newHTTPClient(server.URL, false)
	client.accessToken = "fixture"
	_, err := client.get(context.Background(), "/terms?secret=hidden")
	var status *HTTPError
	if !errors.Is(err, ErrUnavailable) || !errors.As(err, &status) || status.StatusCode != 429 ||
		status.RetryAfter != 120*time.Second || calls.Load() != 1 {
		t.Fatalf("error = %v, status = %+v, calls = %d", err, status, calls.Load())
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("sensitive upstream data leaked: %v", err)
	}

	err = client.Authenticate(context.Background(), Credentials{Login: "fixture", Password: "fixture"})
	if !errors.Is(err, ErrUnavailable) || calls.Load() != 2 {
		t.Fatalf("login retry or error classification incorrect: calls = %d, err = %v", calls.Load(), err)
	}
}

func TestNeverFollowLoginRedirectToAnotherHost(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		leaked.Store(true)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	client := newHTTPClient(server.URL, false)
	err := client.Authenticate(context.Background(), Credentials{Login: "fixture", Password: "fixture"})
	var status *HTTPError
	if !errors.As(err, &status) || status.StatusCode != 307 || leaked.Load() {
		t.Fatalf("redirect was not rejected: err = %v, redirected = %v", err, leaked.Load())
	}
}

func TestDecodeTermsFailsOnUnreadableResults(t *testing.T) {
	for _, body := range []string{
		`{"success":false,"termsForService":null}`,
		`{"success":false,"termsForService":{"termsForDays":[]}}`,
		`{"success":true,"renamed":{}}`,
		`{"termsForService":{}}`,
		`{"termsForService":{"termsForDays":[{"terms":[{"dateTimeFrom":"broken"}]}]}}`,
	} {
		if _, err := decodeTermsResponse(domain.Watch{}, []byte(body), false); !errors.Is(err, ErrResponse) {
			t.Errorf("decode %s: %v", body, err)
		}
	}
	preview, err := decodeTermsResponse(domain.Watch{}, []byte(`{"termsForService":{"termsForDays":[]}}`), false)
	if err != nil || len(preview.apps) != 0 {
		t.Fatalf("empty calendar: apps = %v, err = %v", preview.apps, err)
	}
}

func TestSearchAppointmentsKeepsDifferentAddressesWithoutClinicID(t *testing.T) {
	day := startOfDay(time.Now()).AddDate(0, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != TermsURL {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		at := day.Format(dateFormat) + "T10:15:00"
		fmt.Fprintf(w, `{"success":true,"termsForService":{"termsForDays":[{"terms":[
			{"dateTimeFrom":%q,"doctor":{"id":42},"clinic":"Clinic","address":"Main 1"},
			{"dateTimeFrom":%q,"doctor":{"id":42},"clinic":"Clinic","address":"Side 2"},
			{"dateTimeFrom":%q,"doctor":{"id":42},"clinic":"Clinic","address":" MAIN 1 "},
			{"dateTimeFrom":%q,"doctor":{"id":42},"clinic":"Clinic","address":"Other","clinicId":7},
			{"dateTimeFrom":%q,"doctor":{"id":42},"clinic":"Clinic","address":"Other corrected","clinicId":7}
		]}]}}`, at, at, at, at, at)
	}))
	defer server.Close()

	client := newHTTPClient(server.URL, false)
	client.accessToken = "fixture"
	result, err := client.SearchAppointments(t.Context(), domain.Watch{
		CityID: 1, ServiceID: 2, DateFrom: &day, DateTo: &day,
	})
	if err != nil {
		t.Fatal(err)
	}
	apps := result.Appointments
	if len(apps) != 3 || apps[0].Address != "Main 1" || apps[1].Address != "Side 2" || apps[2].FacilityID != 7 {
		t.Fatalf("search did not distinguish address fallback from a known clinic ID: %+v", apps)
	}
	legacy := apps[0]
	legacy.Address = ""
	if legacyHash := Fingerprint(legacy); apps[0].LegacyFingerprint != legacyHash ||
		apps[1].LegacyFingerprint != legacyHash || apps[2].LegacyFingerprint != "" {
		t.Fatal("search did not preserve the legacy lookup key for missing clinic IDs")
	}
}

func TestSearchReportsPasswordRenewalBeforeUnavailableResponse(t *testing.T) {
	var logins, terms atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LoginURL:
			logins.Add(1)
			fmt.Fprint(w, `{"succeded":true,"token":"fresh-token"}`)
		case XsrfTokenURL:
			fmt.Fprint(w, `{"token":"xsrf"}`)
		case TermsURL:
			if terms.Add(1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("authorization-token") != "fresh-token" {
				t.Error("retry did not use the renewed token")
			}
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := newHTTPClient(server.URL, false)
	client.accessToken = "stale-token"
	client.creds = Credentials{Login: "fixture", Password: "fixture"}
	result, err := client.SearchAppointments(t.Context(), domain.Watch{CityID: 1, ServiceID: 2, NextDays: 1})
	if !result.Authenticated || result.Checked || !errors.Is(err, ErrUnavailable) ||
		logins.Load() != 1 || terms.Load() != 2 {
		t.Fatalf("search result=%+v, logins=%d, terms=%d, err=%v", result, logins.Load(), terms.Load(), err)
	}
}
