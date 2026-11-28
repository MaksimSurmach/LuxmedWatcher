package luxmed

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func TestBookingPortalHoldAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hold    string
		confirm string
		want    error
		paths   []string
	}{
		{"covered visit", `{"hasErrors":false,"value":{"temporaryReservationId":127,"valuations":[{"valuationType":1,"price":0}]}}`,
			`{"hasErrors":false,"value":{"reservationId":987}}`, nil, []string{lockTermURL, confirmTermURL}},
		{"lost response after confirm", `{"hasErrors":false,"value":{"temporaryReservationId":127,"valuations":[{"valuationType":1,"price":0}]}}`,
			`{"hasErrors":false,"value":null}`, ErrBookingUnknown, []string{lockTermURL, confirmTermURL}},
		{"already taken", `{"hasErrors":true,"errors":[{"code":11000083}],"value":null}`,
			"", ErrSlotTaken, []string{lockTermURL}},
		{"paid visit", `{"hasErrors":false,"value":{"temporaryReservationId":127,"valuations":[{"valuationType":2,"price":120}]}}`,
			"", ErrBookingInPortal, []string{lockTermURL, releaseTermURL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				if r.Method != http.MethodPost || r.Header.Get("XSRF-TOKEN") != "synthetic" {
					t.Error("missing booking method or anti-forgery header")
				}
				switch r.URL.Path {
				case lockTermURL:
					fmt.Fprint(w, tc.hold)
				case confirmTermURL:
					var body map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&body) != nil || string(body["temporaryReservationId"]) != "127" {
						t.Error("confirmation did not use the temporary reservation")
					}
					fmt.Fprint(w, tc.confirm)
				case releaseTermURL:
					if r.URL.Query().Get("reservationId") != "127" {
						t.Error("release did not use the temporary reservation")
					}
					fmt.Fprint(w, "{}")
				}
			}))
			defer server.Close()
			client := newHTTPClient(server.URL, false)
			client.xsrfToken = "synthetic"
			id, err := client.BookAppointment(t.Context(), domain.Appointment{
				DateTime: time.Now().Add(time.Hour), BookingDetails: []byte(`{"scheduleId":42}`),
			})
			close(paths)
			var got []string
			for path := range paths {
				got = append(got, path)
			}
			if !errors.Is(err, tc.want) || !slices.Equal(got, tc.paths) || (tc.want == nil && id != "987") {
				t.Fatalf("result %q, %v, paths %v", id, err, got)
			}
		})
	}
}
