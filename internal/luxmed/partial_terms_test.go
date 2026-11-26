package luxmed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func TestSearchLoadsOnlyBoundedMissingDays(t *testing.T) {
	today := startOfDay(time.Now()).AddDate(0, 0, 1)
	var days []string
	for offset := range 7 {
		days = append(days, today.AddDate(0, 0, offset).Format(dateFormat))
	}
	var termInfo []string
	for _, day := range days[1:6] {
		termInfo = append(termInfo, fmt.Sprintf(`{"day":%q,"termsStatus":0,"isLimitedDay":true,"termsCounter":{"termsNumber":1}}`, day))
	}
	termInfo = append(termInfo,
		fmt.Sprintf(`{"day":%q,"termsStatus":0,"termsCounter":{"termsNumber":1}}`, days[0]),
		fmt.Sprintf(`{"day":%q,"termsStatus":3,"isLimitedDay":true,"termsCounter":{"termsNumber":0}}`, days[6]))

	var calls atomic.Int32
	var processID atomic.Value
	validUUID := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Has("pnmExecutionId") || q.Has("referralId") {
			t.Error("unverified PNM or referral context was sent")
		}
		if q.Get("searchPlace.name") != "Example City" || q.Get("searchPlace.id") != "1" || q.Get("serviceVariantId") != "2" {
			t.Errorf("incorrect search context: %v", q)
		}
		switch r.URL.Path {
		case TermsURL:
			processID.Store(q.Get("processId"))
			if !validUUID.MatchString(q.Get("processId")) {
				t.Error("preview lacks its own correlation ID")
			}
			fmt.Fprintf(w, `{"termsForService":{"termsForDays":[{"day":%q,"terms":[{"dateTimeFrom":"%sT10:00:00"}]}],"termsInfoForDays":[%s]}}`,
				days[0], days[0], strings.Join(termInfo, ","))
		case oneDayTermsURL:
			n := int(calls.Add(1))
			if n > maxDayDetailRequests {
				t.Error("detail request budget was exceeded")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if q.Get("searchDateFrom") != days[n] || q.Get("searchDateTo") != days[n] || q.Get("expectedTermsNumber") != "1" ||
				q.Get("processId") != processID.Load() || q.Has("searchDatePreset") || q.Has("nextSearch") {
				t.Errorf("incorrect detail context: %v", q)
			}
			fmt.Fprintf(w, `{"serviceVariantId":2,"termsForDay":{"day":%q,"terms":[{"dateTimeFrom":"%sT11:00:00"}]}}`, days[n], days[n])
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	from, to := today, today.AddDate(0, 0, 6)
	client := newHTTPClient(server.URL, false)
	client.accessToken = "fixture"
	result, err := client.SearchAppointments(context.Background(), domain.Watch{
		CityID: 1, CityName: "Example City", ServiceID: 2, DateFrom: &from, DateTo: &to,
	})
	apps := result.Appointments
	if !result.Checked || !errors.Is(err, ErrPartialResults) || len(apps) != 5 || calls.Load() != 4 {
		t.Fatalf("checked = %v, appointments = %d, requests = %d, err = %v",
			result.Checked, len(apps), calls.Load(), err)
	}
}

func TestSearchSkipsDetailDaysExcludedByWeekday(t *testing.T) {
	sunday := startOfDay(time.Now()).AddDate(0, 0, 1)
	for sunday.Weekday() != time.Sunday {
		sunday = sunday.AddDate(0, 0, 1)
	}
	monday := sunday.AddDate(0, 0, 1)
	sundayDate := sunday.Format(dateFormat)
	mondayDate := monday.Format(dateFormat)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case TermsURL:
			fmt.Fprintf(w, `{"termsForService":{"termsForDays":[],"termsInfoForDays":[
				{"day":%q,"termsStatus":0,"isLimitedDay":true,"termsCounter":{"termsNumber":1}},
				{"day":%q,"termsStatus":0,"isLimitedDay":true,"termsCounter":{"termsNumber":1}}
			]}}`, sundayDate, mondayDate)
		case oneDayTermsURL:
			calls.Add(1)
			if r.URL.Query().Get("searchDateFrom") != sundayDate {
				t.Error("requested detail for excluded weekday")
			}
			fmt.Fprintf(w, `{"serviceVariantId":2,"termsForDay":{"day":%q,"terms":[{"dateTimeFrom":"%sT11:00:00"}]}}`, sundayDate, sundayDate)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newHTTPClient(server.URL, false)
	client.accessToken = "fixture"
	result, err := client.SearchAppointments(context.Background(), domain.Watch{
		CityID: 1, ServiceID: 2, DateFrom: &sunday, DateTo: &monday,
		TimeWindows: []domain.TimeWindow{{Weekday: 7, From: "10:00", To: "12:00"}},
	})
	apps := result.Appointments
	if err != nil || len(apps) != 1 || calls.Load() != 1 {
		t.Fatalf("appointments = %d, calls = %d, err = %v", len(apps), calls.Load(), err)
	}
}

func TestSearchPreservesPreviewWhenDetailRequestsAreRateLimited(t *testing.T) {
	today := startOfDay(time.Now()).AddDate(0, 0, 1)
	first := today.Format(dateFormat)
	second := today.AddDate(0, 0, 1).Format(dateFormat)
	third := today.AddDate(0, 0, 2).Format(dateFormat)
	var detailCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case TermsURL:
			fmt.Fprintf(w, `{"termsForService":{"termsForDays":[{"day":%q,"terms":[{"dateTimeFrom":"%sT10:00:00"}]}],"termsInfoForDays":[
				{"day":%q,"termsStatus":0,"isLimitedDay":true,"termsCounter":{"termsNumber":1}},
				{"day":%q,"termsStatus":0,"isLimitedDay":true,"termsCounter":{"termsNumber":1}}
			]}}`, first, first, second, third)
		case oneDayTermsURL:
			detailCalls.Add(1)
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newHTTPClient(server.URL, false)
	client.accessToken = "fixture"
	result, err := client.SearchAppointments(context.Background(), domain.Watch{CityID: 1, ServiceID: 2, DateFrom: &today, NextDays: 3})
	apps := result.Appointments
	var status *HTTPError
	if !errors.Is(err, ErrPartialResults) || !errors.As(err, &status) || status.RetryAfter != time.Minute ||
		len(apps) != 1 || detailCalls.Load() != 1 {
		t.Fatalf("appointments = %d, calls = %d, error = %v, status = %+v", len(apps), detailCalls.Load(), err, status)
	}
}

func TestDecodeOneDayTermsChecksRequestedDate(t *testing.T) {
	day := time.Date(2026, 5, 6, 0, 0, 0, 0, Location())
	watch := domain.Watch{ServiceID: 2}
	for _, body := range []string{
		`{"serviceVariantId":2,"termsForDay":{"day":"2026-05-06"}}`,
		`{"serviceVariantId":3,"termsForDay":{"day":"2026-05-06","terms":[]}}`,
		`{"serviceVariantId":2,"termsForDay":{"day":"2026-05-07","terms":[]}}`,
		`{"serviceVariantId":2,"termsForDay":{"day":"2026-05-06","terms":[{"dateTimeFrom":"2026-05-07T11:00:00"}]}}`,
	} {
		if _, err := decodeOneDayTerms(watch, day, []byte(body), false); !errors.Is(err, ErrResponse) {
			t.Errorf("decode %s: %v", body, err)
		}
	}
	apps, err := decodeOneDayTerms(watch, day, []byte(`{"serviceVariantId":2,"termsForDay":{"day":"2026-05-06","terms":[]}}`), false)
	if err != nil || len(apps) != 0 {
		t.Fatalf("empty day appointments = %v, err = %v", apps, err)
	}
}

func TestMalformedPreviewKeepsVerifiedSlots(t *testing.T) {
	occasion := startOfDay(time.Now()).AddDate(0, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"termsForService":{"termsForDays":[{"terms":[{"dateTimeFrom":"%sT10:00:00"},{"dateTimeFrom":"invalid"}]}]}}`,
			occasion.Format(dateFormat))
	}))
	defer server.Close()

	client := newHTTPClient(server.URL, false)
	client.accessToken = "fixture"
	result, err := client.SearchAppointments(context.Background(), domain.Watch{
		CityID: 1, ServiceID: 2, DateFrom: &occasion, DateTo: &occasion,
	})
	apps := result.Appointments
	if !result.Checked || !errors.Is(err, ErrPartialResults) || len(apps) != 1 || apps[0].BookingURL != ReservationPageURL {
		t.Fatalf("checked = %v, verified slots = %d, error = %v", result.Checked, len(apps), err)
	}
}
