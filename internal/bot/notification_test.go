package bot

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

var widgetButton = regexp.MustCompile(`<button[^>]*data-callback="([^"]+)"[^>]*>([^<]+)</button>`)

func slot(city, clinic, address, doctor string, day, hour, minute int) domain.AppointmentHistory {
	return domain.AppointmentHistory{Appointment: domain.Appointment{
		CityName: city, FacilityName: clinic, Address: address, DoctorName: doctor,
		DateTime: time.Date(2030, 1, day, hour, minute, 0, 0, luxmed.Location()),
	}}
}

func TestNotificationSingleSlot(t *testing.T) {
	b := testBot(t)
	text, shown := b.appointmentNotification("en", domain.Watch{Name: "W"}, []domain.AppointmentHistory{slot("Krakow", "Clinic A", "Main 1", "Dr A", 5, 9, 30)}, 1, 10)
	if shown != 1 || strings.Contains(text, "More options") {
		t.Fatalf("shown=%d text=%q", shown, text)
	}
	for _, want := range []string{"<b>Krakow</b>\nClinic A\nMain 1\n<b>Sat 05.01</b>\n09:30 · Dr A"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestNotificationGroupsSameStreetOnceAndSortsTimes(t *testing.T) {
	b := testBot(t)
	items := []domain.AppointmentHistory{
		slot("Krakow", "Clinic A", " Main  1 ", "Dr B", 6, 8, 0),
		slot("Krakow", "Clinic A", "MAIN 1", "Dr A", 5, 14, 0),
		slot("Krakow", "Clinic A", "Main 1", "Dr A", 5, 9, 0),
	}
	text, shown := b.appointmentNotification("en", domain.Watch{}, items, 3, 10)
	if shown != 3 || strings.Count(text, "\nMain 1\n") != 1 || strings.Count(text, "Clinic A") != 1 {
		t.Errorf("expected all slots under one original address, got %d in %q", shown, text)
	}
	order := []string{"Sat 05.01", "09:00", "14:00", "Sun 06.01", "08:00"}
	last := -1
	for _, part := range order {
		i := strings.Index(text, part)
		if i <= last {
			t.Fatalf("%q out of order in %q", part, text)
		}
		last = i
	}
}

func TestNotificationKeepsSameTimeInDifferentBranchesDistinct(t *testing.T) {
	b := testBot(t)
	items := []domain.AppointmentHistory{
		slot("Krakow", "Clinic B", "Side 2", "Dr A", 5, 9, 0),
		slot("Krakow", "Clinic A", "Main 1", "Dr A", 5, 9, 0),
		slot("Krakow", "Clinic A", "Main 1", "Dr C", 5, 9, 0),
	}
	text, shown := b.appointmentNotification("en", domain.Watch{}, items, 3, 10)
	if shown != 3 || strings.Count(text, "09:00") != 3 || strings.Index(text, "Clinic A") > strings.Index(text, "Clinic B") {
		t.Errorf("shown=%d text=%q", shown, text)
	}
}

func TestNotificationGroupsFallbackClinicNamesByAddress(t *testing.T) {
	b := testBot(t)
	items := []domain.AppointmentHistory{
		slot("Krakow", "MAIN 1", "MAIN 1", "Dr A", 5, 14, 0),
		slot("Krakow", " Main  1 ", "Main 1", "Dr B", 5, 9, 0),
		slot("Krakow", "Clinic A", "Main 1", "Dr C", 5, 10, 0),
	}
	text, shown := b.appointmentNotification("en", domain.Watch{}, items, len(items), 10)
	for _, block := range []string{
		"\nMain 1\n<b>Sat 05.01</b>\n09:00 · Dr B\n14:00 · Dr A",
		"\nClinic A\nMain 1\n<b>Sat 05.01</b>\n10:00 · Dr C",
	} {
		if !strings.Contains(text, block) {
			t.Errorf("missing block %q in %q", block, text)
		}
	}
	if shown != len(items) || strings.Count(text, "\nMain 1\n") != 2 || strings.Contains(text, "MAIN 1") {
		t.Errorf("expected one address per clinic block, got %d slots in %q", shown, text)
	}
}

func TestNotificationLongListShowsCountPreviewAndFitsTelegram(t *testing.T) {
	b := testBot(t)
	var items []domain.AppointmentHistory
	for i := range 10 {
		items = append(items, slot("Krakow", strings.Repeat("C", 120), strings.Repeat("S", 100), strings.Repeat("D", 80), 1+i, 9, 0))
	}
	text, shown := b.appointmentNotification("en", domain.Watch{Name: strings.Repeat("N", 300)}, items, 40, 10)
	if shown != previewNotificationSlots || !strings.Contains(text, "Appointment times found · 40") || !strings.Contains(text, "More options: 35") {
		t.Errorf("shown=%d text=%q", shown, text)
	}
	// Even a short list must stay below Telegram's limit by dropping what does not fit.
	for i := range 10 {
		items[i].Address = strings.Repeat("Ż", 100)
		items[i].DoctorName = strings.Repeat("Ł", 80)
		items[i].CityName = strings.Repeat("K", 60)
		items[i].FacilityName = strings.Repeat("P", 120) + string(rune('a'+i))
	}
	text, shown = b.appointmentNotification("en", domain.Watch{Name: strings.Repeat("N", 300)}, items, 10, 10)
	if utf8.RuneCountInString(text) > 4096 || shown == 0 || !strings.Contains(text, "More options") && shown < 10 {
		t.Errorf("shown=%d length=%d", shown, utf8.RuneCountInString(text))
	}
}

func TestMultiSlotNotificationShowsCouponsAndHistoryListsAddresses(t *testing.T) {
	ctx := t.Context()
	var sent []string
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/botfake/sendWidget":
			sent = append(sent, r.Form.Get("html"))
		case "/botfake/sendMessage":
			sent = append(sent, r.Form.Get("text"))
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":7}}`)
	})
	user := createUser(t, b, 101, domain.UserStatusActive)
	b.cfg.Browsers = map[int64]*luxmed.Browser{101: {}}
	var err error
	if b.cfg.MiniApp, err = luxmed.NewMiniApp("http://localhost", "https://app.example", ""); err != nil {
		t.Fatal(err)
	}
	watchID, err := b.db.CreateWatch(ctx, domain.Watch{UserID: user.ID, Name: "W", CityID: 1, ServiceID: 12, CheckIntervalSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	items := []domain.AppointmentHistory{
		slot("Krakow", "Clinic", "Main 1", "Dr A", 5, 9, 0),
		slot("Krakow", "Clinic", "Main 1", "Dr A", 5, 11, 0),
		slot("Krakow", "Clinic", "Side 2", "Dr A", 5, 9, 0),
	}
	for i := range items {
		items[i].Fingerprint = fmt.Sprint("f", i)
		items[i].Appointment.Fingerprint = items[i].Fingerprint
		if _, _, err := b.db.UpsertAppointmentHistory(ctx, watchID, items[i].Appointment); err != nil {
			t.Fatal(err)
		}
	}
	if id, err := b.NotifyAppointments(ctx, user, domain.Watch{ID: watchID, UserID: user.ID, Name: "W"}, items, 3); err != nil || id != 7 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if len(sent) != 1 || strings.Count(sent[0], `<a class="open"`) != 3 || !strings.Contains(sent[0], "11:00") {
		t.Fatalf("short list lost coupons: %q", sent)
	}
	b.handleHistoryPage(ctx, user, fmt.Sprintf("history:%d:0", watchID))
	if len(sent) != 2 || !strings.Contains(sent[1], "Main 1") || !strings.Contains(sent[1], "Side 2") {
		t.Fatalf("history lacks addresses: %q", sent)
	}
}

func TestAllOptionsCallbackShowsAvailableSlotsFromItsNotification(t *testing.T) {
	ctx := t.Context()
	type message struct {
		text     string
		keyboard tgbotapi.InlineKeyboardMarkup
	}
	sent := make(chan message, 8)
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/botfake/sendMessage" || r.URL.Path == "/botfake/sendWidget" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			msg := message{text: r.Form.Get("text") + r.Form.Get("caption") + r.Form.Get("html")}
			if markup := r.Form.Get("reply_markup"); markup != "" {
				if err := json.Unmarshal([]byte(markup), &msg.keyboard); err != nil {
					t.Error(err)
				}
			}
			// Widget cards carry their buttons in the HTML instead of a keyboard.
			for _, m := range widgetButton.FindAllStringSubmatch(r.Form.Get("html"), -1) {
				data := html.UnescapeString(m[1])
				msg.keyboard.InlineKeyboard = append(msg.keyboard.InlineKeyboard, []tgbotapi.InlineKeyboardButton{{Text: html.UnescapeString(m[2]), CallbackData: &data}})
			}
			sent <- msg
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":7}}`)
	})
	nextMessage := func() message {
		t.Helper()
		select {
		case msg := <-sent:
			return msg
		default:
			t.Fatal("expected a Telegram message")
			return message{}
		}
	}
	button := func(msg message, label string) string {
		t.Helper()
		for _, row := range msg.keyboard.InlineKeyboard {
			for _, item := range row {
				if item.Text == label && item.CallbackData != nil {
					return *item.CallbackData
				}
			}
		}
		return ""
	}

	user := createUser(t, b, 101, domain.UserStatusActive)
	watch := domain.Watch{UserID: user.ID, Name: "Selected", CityID: 1, ServiceID: 12, CheckIntervalSeconds: 300}
	var err error
	watch.ID, err = b.db.CreateWatch(ctx, watch)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := b.db.CreateWatch(ctx, watch)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(watchID int64, name string, at time.Time) domain.AppointmentHistory {
		t.Helper()
		h, _, err := b.db.UpsertAppointmentHistory(ctx, watchID, domain.Appointment{
			Fingerprint: name, DateTime: at, CityName: "Warsaw", FacilityName: "Clinic",
			DoctorName: "Dr A", Address: name,
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	now := time.Now().Truncate(time.Second)
	expired := insert(watch.ID, "Expired listing", now.Add(72*time.Hour))
	past := insert(watch.ID, "Past appointment", now.Add(-time.Hour))
	hidden := insert(watch.ID, "Hidden listing", now.Add(30*time.Minute))
	if err := b.db.HideAppointmentHistory(ctx, user.ID, hidden.ID); err != nil {
		t.Fatal(err)
	}
	otherWatch := insert(otherID, "Other watch", now.Add(30*time.Minute))

	previous := make([]domain.AppointmentHistory, 0, 5)
	for i := range 5 {
		previous = append(previous, insert(watch.ID, fmt.Sprintf("Previously shown %d", i), now.Add(time.Duration(i+1)*time.Minute)))
	}
	if err := b.db.MarkNotifications(ctx, previous, 6, domain.NotificationStatusSent, ""); err != nil {
		t.Fatal(err)
	}

	items := make([]domain.AppointmentHistory, 0, 11)
	for i := range 11 {
		hour := i + 1
		if hour == 6 {
			hour = 5 // Slots 5 and 6 have the same time across the page boundary.
		}
		items = append(items, insert(watch.ID, fmt.Sprintf("Street %02d", i+1), now.Add(time.Duration(hour)*time.Hour)))
	}
	if _, err := b.NotifyAppointments(ctx, user, watch, items[:3], 3); err != nil {
		t.Fatal(err)
	}
	if button(nextMessage(), "All options") != "" {
		t.Fatal("complete short list exposed All options")
	}
	batch := append(append([]domain.AppointmentHistory(nil), items...), expired, past, hidden)
	notificationID, err := b.NotifyAppointments(ctx, user, watch, items[:10], len(batch))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.MarkNotifications(ctx, append(batch, otherWatch), notificationID, domain.NotificationStatusSent, ""); err != nil {
		t.Fatal(err)
	}
	if err := b.db.MarkMissingAppointments(ctx, watch.ID, time.Now().Add(time.Second), time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range append(append(previous, items...), past) {
		if _, _, err := b.db.UpsertAppointmentHistory(ctx, watch.ID, item.Appointment); err != nil {
			t.Fatal(err)
		}
	}
	notification := nextMessage()
	action := button(notification, "All options")
	if action == "" || !strings.Contains(notification.text, b.catalog.T(user.Locale, "notification.title", len(batch))) ||
		!strings.Contains(notification.text, "Street 03") || strings.Contains(notification.text, "Street 04") {
		t.Fatalf("unexpected long notification: %q (action %q)", notification.text, action)
	}
	click := func(messageID int, action string) {
		t.Helper()
		if action == "" {
			t.Fatal("expected a callback")
		}
		b.handleCallback(ctx, &tgbotapi.CallbackQuery{
			ID: "test", Data: action, From: &tgbotapi.User{ID: user.TelegramUserID},
			Message: &tgbotapi.Message{MessageID: messageID, Chat: &tgbotapi.Chat{ID: user.TelegramChatID, Type: "private"}},
		})
	}
	click(notificationID, action)
	first := nextMessage()
	lastIndex := -1
	for i := range 5 {
		index := strings.Index(first.text, fmt.Sprintf("Street %02d", i+1))
		if index <= lastIndex {
			t.Fatalf("first page is missing the earliest slots in order: %q", first.text)
		}
		lastIndex = index
	}
	for _, excluded := range []string{"Previously shown", "Expired listing", "Past appointment", "Other watch", "Hidden listing", "Street 06"} {
		if strings.Contains(first.text, excluded) {
			t.Errorf("unexpected %q on first page: %q", excluded, first.text)
		}
	}
	if err := b.db.HideAppointmentHistory(ctx, user.ID, items[0].ID); err != nil {
		t.Fatal(err)
	}
	click(50, button(first, "Next"))
	second := nextMessage()
	if !strings.Contains(second.text, "Street 06") || strings.Contains(second.text, "Street 01") {
		t.Errorf("unexpected second page: %q", second.text)
	}
	click(51, button(second, "Previous"))
	previousPage := nextMessage()
	if !strings.Contains(previousPage.text, "Street 02") || !strings.Contains(previousPage.text, "Street 05") ||
		strings.Contains(previousPage.text, "Street 01") || strings.Contains(previousPage.text, "Street 06") {
		t.Errorf("unexpected previous page after slot removal: %q", previousPage.text)
	}
	click(51, button(second, "Next"))
	third := nextMessage()
	if !strings.Contains(third.text, "Street 11") || strings.Contains(third.text, "Street 06") {
		t.Errorf("unexpected third page: %q", third.text)
	}
	click(52, button(third, b.catalog.T(user.Locale, "button.hide")))
	if msg := nextMessage(); msg.text != b.catalog.T(user.Locale, "options.empty") {
		t.Errorf("hiding the last slot should leave the page empty: %q", msg.text)
	} else {
		click(53, button(msg, "Previous"))
		if previous := nextMessage(); !strings.Contains(previous.text, "Street 06") || strings.Contains(previous.text, "Previously shown") {
			t.Errorf("unexpected previous page: %q", previous.text)
		}
	}
}

func TestAcceptedCheckAcknowledgedInSavedLocale(t *testing.T) {
	ctx := t.Context()
	acks := make(chan string, 4)
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/botfake/answerCallbackQuery":
			acks <- r.Form.Get("text")
		case "/botfake/answerWidgetQuery":
			acks <- r.Form.Get("data")
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":7}}`)
	})
	owner := createUser(t, b, 41, domain.UserStatusActive)
	other := createUser(t, b, 42, domain.UserStatusActive)
	if err := b.db.SetUserLocale(ctx, owner.ID, "pl"); err != nil {
		t.Fatal(err)
	}
	id, err := b.db.CreateWatch(ctx, domain.Watch{UserID: owner.ID, Name: "W", CityID: 1, ServiceID: 12, CheckIntervalSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("watch:check:%d", id)
	for _, test := range []struct {
		actor domain.User
		want  string
	}{{owner, "Sprawdzam…"}, {other, ""}} {
		from := &tgbotapi.User{ID: test.actor.TelegramUserID}
		chat := &tgbotapi.Chat{ID: from.ID, Type: "private"}
		b.handleCallback(ctx, &tgbotapi.CallbackQuery{ID: "cb", Data: data, From: from, Message: &tgbotapi.Message{Chat: chat}})
		query := widgetQuery{ID: "w", From: from, Chat: chat, MessageID: 1, Data: data}
		query.Type.Type = "data"
		b.handleWidgetQuery(ctx, &query)
		b.workers.Wait()
		for range 2 {
			select {
			case got := <-acks:
				if got != test.want {
					t.Errorf("user %d: acknowledgement %q, want %q", from.ID, got, test.want)
				}
			default:
				t.Fatalf("missing acknowledgement for user %d", from.ID)
			}
		}
	}
}
