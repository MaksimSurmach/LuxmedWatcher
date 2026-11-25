package bot

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

func TestAppointmentWidgetDelivery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		response  string
		messageID int
		fallback  bool
	}{
		{"widget", 200, `{"ok":true,"result":{"message_id":51}}`, 51, false},
		{"unsupported method", 404, `{"ok":false,"error_code":404}`, 93, true},
		{"rejected widget", 400, `{"ok":false,"error_code":400}`, 93, true},
		{"server error", 502, `{"ok":false,"error_code":502}`, 0, false},
		{"ambiguous server error", 502, `{"ok":false,"error_code":400}`, 0, false},
		{"rate limit", 429, `{"ok":false,"error_code":429}`, 0, false},
		{"unparseable response", 200, `not JSON`, 0, false},
		{"no message ID", 200, `{"ok":true,"result":{}}`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			widgetSends, fallbackSends := 0, 0
			var counts sync.Mutex
			b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
				counts.Lock()
				defer counts.Unlock()
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("parse_mode") != "HTML" || !strings.Contains(r.Form.Get("reply_markup"), "watch:pause:30") || !strings.Contains(r.Form.Get("reply_markup"), "watch:check:30") {
					t.Errorf("missing HTML parse mode or pause control in %s", r.Form.Encode())
				}
				switch r.URL.Path {
				case "/botfake/sendWidget":
					widgetSends++
					markup := r.Form.Get("html")
					for _, part := range []string{"<style>", "--tg-theme-button-color", "TelegramWidget.request", "booking-hint", "app:history", "A &lt; B", "&lt;script&gt;", luxmed.ReservationPageURL} {
						if !strings.Contains(markup, part) {
							t.Errorf("missing %q in widget HTML", part)
						}
					}
					if !strings.Contains(r.Form.Get("caption"), "Street 12") || r.Form.Get("allow_queries") != "true" {
						t.Error("widget lacks a readable caption or querying is disabled")
					}
					var compressed bytes.Buffer
					zipper, err := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
					if err != nil {
						t.Error(err)
					} else {
						_, _ = zipper.Write([]byte(markup))
						_ = zipper.Close()
						if len(markup) > 32768 || compressed.Len() > 4096 {
							t.Errorf("widget exceeds Telegram limits: %d bytes, %d compressed", len(markup), compressed.Len())
						}
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.response)
				case "/botfake/sendMessage":
					fallbackSends++
					if !strings.Contains(r.Form.Get("text"), "Street 12") || r.Form.Get("disable_web_page_preview") != "true" {
						t.Error("fallback lost appointment details or preview setting")
					}
					fmt.Fprint(w, `{"ok":true,"result":{"message_id":93}}`)
				default:
					t.Errorf("unexpected method %s", r.URL.Path)
				}
			})
			user := createUser(t, b, 101, domain.UserStatusActive)
			b.cfg.Browsers = map[int64]*luxmed.Browser{101: {}}
			var err error
			b.cfg.MiniApp, err = luxmed.NewMiniApp("http://localhost", "https://app.example", "")
			if err != nil {
				t.Fatal(err)
			}
			watch := domain.Watch{ID: 30, UserID: user.ID, Name: "A < B"}
			history := []domain.AppointmentHistory{{Appointment: domain.Appointment{
				DateTime: time.Unix(0, 0), DoctorName: "<script>", FacilityName: "A & B", Address: "Street 12", BookingURL: "https://other.example",
			}}}
			gotID, err := b.NotifyAppointments(t.Context(), user, watch, history, 1)
			if gotID != tc.messageID || (err != nil) != (tc.messageID == 0) {
				t.Fatalf("got ID %d and err %v, want ID %d", gotID, err, tc.messageID)
			}
			counts.Lock()
			defer counts.Unlock()
			if widgetSends != 1 || (tc.fallback && fallbackSends != 1) || (!tc.fallback && fallbackSends != 0) {
				t.Errorf("unexpected attempts: widgets=%d, fallback=%d", widgetSends, fallbackSends)
			}
		})
	}
}

func TestPollWidgetQueriesAndReuseLanguageAction(t *testing.T) {
	events := make(chan string, 10)
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/botfake/getUpdates":
			if !strings.Contains(r.Form.Get("allowed_updates"), "widget_query") {
				t.Error("poll request did not opt in to widget_query")
			}
			fmt.Fprint(w, `{"ok":true,"result":[
				{"update_id":20,"widget_query":{"id":"wrong-chat","from":{"id":101},"chat":{"id":202,"type":"private"},"message_id":40,"type":{"type":"data"},"data":"lang:uk"}},
				{"update_id":21,"widget_query":{"id":"unknown-action","from":{"id":101},"chat":{"id":101,"type":"private"},"message_id":41,"type":{"type":"data"},"data":"watch:delete:123"}},
				{"update_id":22,"widget_query":{"id":"valid","from":{"id":101},"chat":{"id":101,"type":"private"},"message_id":42,"type":{"type":"data"},"data":"lang:be"}},
				{"update_id":23,"message":{"text":"/menu","chat":{"id":101,"type":"private"}}}
			]}`)
		case "/botfake/answerWidgetQuery":
			events <- r.Form.Get("widget_query_id")
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case "/botfake/sendMessage":
			events <- "message"
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":50}}`)
		default:
			t.Errorf("unexpected method %s", r.URL.Path)
		}
	})
	createUser(t, b, 101, domain.UserStatusPendingInvite)
	config := tgbotapi.NewUpdate(20)
	config.AllowedUpdates = []string{"message", "callback_query", "widget_query"}
	updates, err := b.getUpdates(config)
	if err != nil || len(updates) != 4 || updates[3].Message == nil || updates[3].UpdateID != 23 {
		t.Fatalf("malformed updates: %+v, %v", updates, err)
	}
	for _, update := range updates[:3] {
		if update.WidgetQuery == nil {
			t.Fatalf("missing widget query in update %d", update.UpdateID)
		}
		b.handleWidgetQuery(t.Context(), update.WidgetQuery)
	}
	if len(events) != 2 {
		t.Fatalf("expected an acknowledgement and a message, got %d events", len(events))
	}
	if first, second := <-events, <-events; first != "valid" || second != "message" {
		t.Fatalf("unexpected event sequence: %s, %s", first, second)
	}
	user, err := b.db.UserByTelegramID(t.Context(), 101)
	if err != nil || user.Locale != "be" || user.IsActive() {
		t.Fatalf("unexpected user after language selection: %+v, %v", user, err)
	}
}

func TestBookCommandOpensOfficialReservationPage(t *testing.T) {
	sent := make(chan struct{}, 1)
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/botfake/sendWidget" ||
			!strings.Contains(r.Form.Get("html"), luxmed.ReservationPageURL) ||
			!strings.Contains(r.Form.Get("reply_markup"), luxmed.ReservationPageURL) {
			t.Errorf("booking entry must open the official portal from the widget and inline keyboard")
		}
		sent <- struct{}{}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":51}}`)
	})
	user := createUser(t, b, 101, domain.UserStatusActive)
	b.handleCommand(t.Context(), user, &tgbotapi.Message{
		Text: "/book", Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Length: 5}},
	})
	select {
	case <-sent:
	default:
		t.Fatal("booking link was not sent")
	}
}
