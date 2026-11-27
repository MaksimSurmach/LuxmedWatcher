package bot

import (
	"fmt"
	"html"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

// Telegram caps a message at 4096 characters; keep headroom for the tail line.
const notificationBudget = 3900

const appointmentTimeLayout = "2006-01-02 15:04"

// warsaw is LuxMed's clock: slot times arrive in it, so stored UTC times are shown in it too.
// The embedded tzdata keeps this working on the alpine runtime image, which ships no zoneinfo.
var warsaw = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// translate is catalog.T bound to the reader's locale.
type translate func(key string, args ...any) string

// esc makes user and LuxMed data safe inside Telegram HTML.
var esc = html.EscapeString

// slotTime renders "Pt 12.12 14:30": people plan by weekday, and slots are at most weeks away.
func slotTime(t translate, when time.Time) string {
	days := strings.Split(t("weekdays.short"), ",")
	if len(days) != 7 {
		return when.Format("02.01 15:04")
	}
	return days[when.Weekday()] + " " + when.Format("02.01 15:04")
}

// appointmentHTML is the one shape every appointment takes: the time leads in bold
// because it is what the reader scans for, the service trails as quiet metadata.
func appointmentHTML(t translate, prefix string, h domain.AppointmentHistory) string {
	place := esc(h.FacilityName)
	if h.Address != "" {
		place += ", " + esc(h.Address)
	}
	return fmt.Sprintf("<b>%s%s</b>\n%s\n%s\n<i>%s</i>",
		prefix, slotTime(t, h.DateTime), esc(h.DoctorName), place, esc(h.ServiceName))
}

// appointmentsHTML renders the found-appointments notification. Items that would
// overflow the budget are dropped whole, so a cut never lands inside a tag.
func appointmentsHTML(t translate, watch domain.Watch, histories []domain.AppointmentHistory, totalFound int) string {
	var text strings.Builder
	fmt.Fprintf(&text, "<b>%s</b>\n\n", esc(t("notification.appointment_found.title")))
	text.WriteString(t("notification.count", totalFound) + "\n")
	if strings.TrimSpace(watch.Name) != "" {
		text.WriteString(t("notification.watch", esc(watch.Name)) + "\n")
	} else {
		text.WriteString(t("notification.service_city", esc(watch.ServiceName), esc(watch.CityName)) + "\n")
	}
	shown := 0
	for i, h := range histories {
		item := "\n\n" + appointmentHTML(t, fmt.Sprintf("%d. ", i+1), h)
		if utf8.RuneCountInString(text.String())+utf8.RuneCountInString(item) > notificationBudget {
			break
		}
		text.WriteString(item)
		shown++
	}
	if rest := totalFound - shown; rest > 0 {
		text.WriteString("\n\n<i>" + t("notification.more", rest) + "</i>")
	}
	return text.String()
}

func historyHTML(t translate, items []domain.AppointmentHistory) string {
	lines := make([]string, 0, len(items))
	for _, h := range items {
		lines = append(lines, appointmentHTML(t, "", h))
	}
	return strings.Join(lines, "\n\n")
}
