package bot

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
)

func TestBookingActionsInTelegramWidgetsAndFallback(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint("fallback=", fallback), func(t *testing.T) {
			requests := make(chan string, 3)
			b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("parse_mode") != "HTML" {
					t.Error("missing HTML parse mode")
				}
				if strings.HasSuffix(r.URL.Path, "/sendWidget") {
					if fallback {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"ok":false,"error_code":400}`)
						return
					}
					requests <- r.Form.Get("html") + " " + r.Form.Get("caption")
				} else {
					requests <- r.Form.Get("reply_markup") + " " + r.Form.Get("text")
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":93}}`)
			})
			b.SetRunner(&scheduler.Runner{})
			user := createUser(t, b, 101, domain.UserStatusActive)
			now := time.Now()
			slot := domain.AppointmentHistory{ID: 17, Appointment: domain.Appointment{
				DateTime: now.Add(30 * time.Hour), DoctorName: "<Doctor>", Address: "Street 12",
			}}
			_, err := b.NotifyAppointments(t.Context(), user, domain.Watch{ID: 3, UserID: user.ID}, []domain.AppointmentHistory{slot}, 1)
			if err != nil {
				t.Fatal(err)
			}
			if msg := <-requests; !strings.Contains(msg, "book:17") || strings.Contains(msg, "<Doctor>") {
				t.Errorf("slot card missing booking action or safe escaping")
			}

			if err := b.NotifyBooking(t.Context(), user, domain.Booking{
				ID: 11, UserID: user.ID, DateTime: slot.DateTime, Doctor: "<Doctor>", Service: "Checkup",
			}, "confirmed"); err != nil {
				t.Fatal(err)
			}
			if msg := <-requests; !strings.Contains(msg, "remind:11") || strings.Contains(msg, "<Doctor>") {
				t.Errorf("confirmation card missing reminder action or safe escaping")
			}
		})
	}
}
