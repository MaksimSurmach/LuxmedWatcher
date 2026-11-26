package bot

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
)

func TestNotifyLuxMedReconnectDeliveryOutcomes(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		response   string
		wantError  string
		retryAfter time.Duration
	}{
		{
			name: "accepted", status: 200,
			response: `{"ok":true,"result":{"message_id":42}}`,
		},
		{
			name: "rate limited", status: 429,
			response:   `{"ok":false,"error_code":429,"parameters":{"retry_after":123}}`,
			wantError:  "telegram temporarily rejected reconnect warning",
			retryAfter: 123 * time.Second,
		},
		{
			name: "blocked", status: 403,
			response:  `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`,
			wantError: "telegram permanently rejected reconnect warning",
		},
		{
			name: "unknown", status: 502, response: "invalid body",
			wantError: "telegram reconnect delivery outcome unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/botfake/sendMessage" || r.Form.Get("chat_id") != "101" ||
					!strings.Contains(r.Form.Get("reply_markup"), `"callback_data":"account"`) {
					t.Errorf("unexpected recipient or reconnect action: %s %v", r.URL.Path, r.Form)
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.response)
			})
			user := createUser(t, b, 101, domain.UserStatusActive)
			err := b.NotifyLuxMedReconnect(t.Context(), user)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("delivery error = %v, want %s", err, test.wantError)
			}
			rejection, retryable := errors.AsType[*scheduler.ReconnectDeliveryRejected](err)
			if retryable != (test.retryAfter > 0) || retryable && rejection.RetryAfter != test.retryAfter {
				t.Fatalf("incorrect retry classification: %v", rejection)
			}
		})
	}
}
