package bot

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

func slot(city, clinic, address, doctor string, day, hour, minute int) domain.AppointmentHistory {
	return domain.AppointmentHistory{Appointment: domain.Appointment{
		CityName: city, FacilityName: clinic, Address: address, DoctorName: doctor,
		DateTime: time.Date(2030, 1, day, hour, minute, 0, 0, luxmed.Location()),
	}}
}

func TestNotificationSingleSlot(t *testing.T) {
	b := testBot(t)
	text, shown := b.appointmentNotification("en", domain.Watch{Name: "W"}, []domain.AppointmentHistory{slot("Krakow", "Clinic A", "Main 1", "Dr A", 5, 9, 30)}, 1, 10)
	if shown != 1 || strings.Contains(text, "More in history") {
		t.Fatalf("shown=%d text=%q", shown, text)
	}
	for _, want := range []string{"<b>Krakow</b>\nClinic A\nMain 1\n<b>05.01.2030</b>\n09:30 · Dr A"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestNotificationGroupsSameStreetOnceAndSortsTimes(t *testing.T) {
	b := testBot(t)
	items := []domain.AppointmentHistory{
		slot("Krakow", "Clinic A", "Main 1", "Dr B", 6, 8, 0),
		slot("Krakow", "Clinic A", "Main 1", "Dr A", 5, 14, 0),
		slot("Krakow", "Clinic A", "Main 1", "Dr A", 5, 9, 0),
	}
	text, _ := b.appointmentNotification("en", domain.Watch{}, items, 3, 10)
	if strings.Count(text, "Main 1") != 1 || strings.Count(text, "Clinic A") != 1 {
		t.Errorf("address repeated: %q", text)
	}
	order := []string{"05.01.2030", "09:00", "14:00", "06.01.2030", "08:00"}
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

func TestNotificationLongListShowsCountPreviewAndFitsTelegram(t *testing.T) {
	b := testBot(t)
	var items []domain.AppointmentHistory
	for i := range 10 {
		items = append(items, slot("Krakow", strings.Repeat("C", 120), strings.Repeat("S", 100), strings.Repeat("D", 80), 1+i, 9, 0))
	}
	text, shown := b.appointmentNotification("en", domain.Watch{Name: strings.Repeat("N", 300)}, items, 40, 10)
	if shown != previewNotificationSlots || !strings.Contains(text, "LuxMed appointments found: 40") || !strings.Contains(text, "More in history: 35") {
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
	if utf8.RuneCountInString(text) > 4096 || shown == 0 || !strings.Contains(text, "More in history") && shown < 10 {
		t.Errorf("shown=%d length=%d", shown, utf8.RuneCountInString(text))
	}
}

func TestMultiSlotNotificationUsesTextAndAllOptionsListsAddresses(t *testing.T) {
	ctx := t.Context()
	var sent []string
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/botfake/sendWidget":
			t.Error("grouped list sent through widget")
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
	if len(sent) != 1 || !strings.Contains(sent[0], "11:00") {
		t.Fatalf("short list lost slots: %q", sent)
	}
	b.handleHistoryPage(ctx, user, fmt.Sprintf("history:%d:0", watchID))
	if len(sent) != 2 || !strings.Contains(sent[1], "Main 1") || !strings.Contains(sent[1], "Side 2") {
		t.Fatalf("history lacks addresses: %q", sent)
	}
}
