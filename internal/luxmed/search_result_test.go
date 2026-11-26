package luxmed

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSearchResultCheckedAcrossClientModes(t *testing.T) {
	first := startOfDay(time.Now()).AddDate(0, 0, 1)
	secondWindow := first.AddDate(0, 0, 14)
	twoWindows := secondWindow.AddDate(0, 0, 1)
	past := first.AddDate(0, 0, -3)
	beyondHorizon := first.AddDate(0, 0, 100)
	type response struct {
		path   string
		status int
		body   string
		err    error
	}
	preview := func(terms, info string) response {
		return response{path: TermsURL, status: http.StatusOK, body: fmt.Sprintf(
			`{"termsForService":{"termsForDays":[{"terms":[%s]}],"termsInfoForDays":[%s]}}`, terms, info,
		)}
	}
	slot := func(day time.Time) string {
		return fmt.Sprintf(`{"dateTimeFrom":%q}`, day.Format(dateFormat)+"T10:00:00")
	}
	const malformed = `{"dateTimeFrom":"invalid"}`
	empty := preview("", "")
	bad := preview(malformed, "")
	unauthorized := response{path: TermsURL, status: http.StatusUnauthorized}
	transportFailure := response{path: TermsURL, err: io.ErrUnexpectedEOF}

	var detailDays []string
	var details []response
	for offset := 1; offset <= maxDayDetailRequests+1; offset++ {
		day := first.AddDate(0, 0, offset).Format(dateFormat)
		detailDays = append(detailDays, fmt.Sprintf(
			`{"day":%q,"isLimitedDay":true,"termsCounter":{"termsNumber":1}}`, day,
		))
		if offset <= maxDayDetailRequests {
			details = append(details, response{path: oneDayTermsURL, status: http.StatusOK, body: fmt.Sprintf(
				`{"serviceVariantId":2,"termsForDay":{"day":%q,"terms":[]}}`, day,
			)})
		}
	}
	limited := append([]response{preview(slot(first), strings.Join(detailDays, ","))}, details...)

	for _, test := range []struct {
		name        string
		from, to    time.Time
		responses   []response
		checked     bool
		slots       int
		wantErr     error
		wantPartial bool
	}{
		{name: "past range skips requests", from: past, to: past},
		{name: "range beyond horizon skips requests", from: beyondHorizon, to: beyondHorizon},
		{name: "valid empty window", responses: []response{empty}, checked: true},
		{name: "first response entirely malformed", responses: []response{bad}, wantErr: ErrResponse},
		{
			name: "valid slot beside malformed time", checked: true, slots: 1,
			responses: []response{preview(slot(first)+","+malformed, "")}, wantErr: ErrResponse, wantPartial: true,
		},
		{
			name: "verified slot outside requested window", checked: true,
			responses: []response{preview(slot(secondWindow)+","+malformed, "")}, wantErr: ErrResponse, wantPartial: true,
		},
		{
			name: "malformed later window after valid empty window", to: twoWindows, checked: true,
			responses: []response{empty, bad}, wantErr: ErrResponse, wantPartial: true,
		},
		{
			name: "detail request limit", to: first.AddDate(0, 0, maxDayDetailRequests+1),
			responses: limited, checked: true, slots: 1, wantErr: ErrPartialResults, wantPartial: true,
		},
		{name: "first request returns 401", responses: []response{unauthorized}, wantErr: ErrAuthentication},
		{
			name: "later 401 retains prior evidence and reports authentication failure", to: twoWindows,
			responses: []response{empty, unauthorized}, checked: true, wantErr: ErrAuthentication, wantPartial: true,
		},
		{
			name:      "transport fails before confirmation",
			responses: []response{transportFailure, transportFailure, transportFailure}, wantErr: ErrUnavailable,
		},
		{
			name: "transport fails after confirmation", to: twoWindows, checked: true,
			responses: []response{empty, transportFailure, transportFailure, transportFailure},
			wantErr:   ErrUnavailable, wantPartial: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, mode := range []string{"browser", "password"} {
				t.Run(mode, func(t *testing.T) {
					t.Parallel()
					client := newHTTPClient("http://luxmed.test", false)
					if mode == "browser" {
						browser, err := NewBrowser("http://luxmed.test", "http://localhost:18769")
						if err != nil {
							t.Fatal(err)
						}
						client = browser.Client()
					} else {
						client.accessToken = "fixture"
						client.creds = Credentials{Login: "fixture", Password: "fixture"}
					}
					requests, logins := 0, 0
					client.client.Transport = searchTransport(func(req *http.Request) (*http.Response, error) {
						if req.Body != nil {
							req.Body.Close()
						}
						reply := unauthorized
						if req.URL.Path == LoginURL {
							logins++
						} else {
							if requests >= len(test.responses) {
								t.Errorf("unexpected request: %s", req.URL.Path)
								return nil, errors.New("unexpected request")
							}
							reply = test.responses[requests]
							requests++
							if req.URL.Path != reply.path {
								t.Errorf("request path = %s, want %s", req.URL.Path, reply.path)
							}
						}
						if reply.err != nil {
							return nil, reply.err
						}
						return &http.Response{
							StatusCode: reply.status, Header: make(http.Header),
							Body: io.NopCloser(strings.NewReader(reply.body)),
						}, nil
					})

					from, to := test.from, test.to
					if from.IsZero() {
						from = first
					}
					if to.IsZero() {
						to = first
					}
					result, err := client.SearchAppointments(t.Context(), domain.Watch{
						CityID: 1, ServiceID: 2, DateFrom: &from, DateTo: &to,
					})
					if result.Checked != test.checked || len(result.Appointments) != test.slots ||
						!errors.Is(err, test.wantErr) || errors.Is(err, ErrPartialResults) != test.wantPartial {
						t.Fatalf("Checked=%t, slots=%d, error=%v; want %t, %d, %v (partial=%t)",
							result.Checked, len(result.Appointments), err, test.checked, test.slots, test.wantErr, test.wantPartial)
					}
					wantLogins := 0
					if mode == "password" && test.wantErr == ErrAuthentication {
						wantLogins = 1
					}
					if requests != len(test.responses) || logins != wantLogins {
						t.Fatalf("searches=%d, logins=%d; want %d, %d", requests, logins, len(test.responses), wantLogins)
					}
				})
			}
		})
	}
}
