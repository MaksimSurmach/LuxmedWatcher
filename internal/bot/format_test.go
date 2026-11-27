package bot

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
)

func TestAppointmentsHTMLEscapesAndFitsBudget(t *testing.T) {
	h := domain.AppointmentHistory{Appointment: domain.Appointment{
		DateTime:     time.Date(2026, 12, 3, 9, 30, 0, 0, time.UTC),
		DoctorName:   "Dr <Jan> & Co",
		FacilityName: strings.Repeat("Placówka ", 40),
		ServiceName:  "Konsultacja",
	}}
	histories := make([]domain.AppointmentHistory, 30)
	for i := range histories {
		histories[i] = h
	}

	cat, err := i18n.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	tr := func(key string, args ...any) string { return cat.T("en", key, args...) }
	got := appointmentsHTML(tr, domain.Watch{Name: "a<b"}, histories, 50)

	if strings.Contains(got, "<Jan>") || strings.Contains(got, "a<b") {
		t.Fatalf("raw data not escaped:\n%s", got)
	}
	if !strings.Contains(got, "<b>1. Thu 03.12 09:30</b>") {
		t.Fatalf("missing bold time line:\n%s", got)
	}
	if n := utf8.RuneCountInString(got); n > 4096 {
		t.Fatalf("message is %d runes, over Telegram limit", n)
	}
	if strings.Count(got, "<b>") != strings.Count(got, "</b>") || strings.Count(got, "<i>") != strings.Count(got, "</i>") {
		t.Fatalf("unbalanced tags:\n%s", got)
	}
	if !strings.Contains(got, "more not shown") {
		t.Fatalf("expected overflow tail line")
	}
}
