package bot

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

// Telegram caps a message at 4096 characters; keep headroom for the tail line.
const notificationBudget = 3900

const appointmentTimeLayout = "2006-01-02 15:04"

// esc makes user and LuxMed data safe inside Telegram HTML.
var esc = html.EscapeString

// appointmentHTML is the one shape every appointment takes: the time leads in bold
// because it is what the reader scans for, the service trails as quiet metadata.
func appointmentHTML(prefix string, h domain.AppointmentHistory) string {
	return fmt.Sprintf("<b>%s%s</b>\n%s\n%s\n<i>%s</i>",
		prefix, h.DateTime.Format(appointmentTimeLayout), esc(h.DoctorName), esc(h.FacilityName), esc(h.ServiceName))
}

// appointmentsHTML renders the found-appointments notification. Items that would
// overflow the budget are dropped whole, so a cut never lands inside a tag.
func appointmentsHTML(title string, watch domain.Watch, histories []domain.AppointmentHistory, totalFound int) string {
	var text strings.Builder
	fmt.Fprintf(&text, "<b>%s</b>\n\n", esc(title))
	fmt.Fprintf(&text, "Найдено терминов: <b>%d</b>\n", totalFound)
	if strings.TrimSpace(watch.Name) != "" {
		fmt.Fprintf(&text, "Наблюдение: %s\n", esc(watch.Name))
	} else {
		fmt.Fprintf(&text, "Услуга: %s, город: %s\n", esc(watch.ServiceName), esc(watch.CityName))
	}
	shown := 0
	for i, h := range histories {
		item := "\n\n" + appointmentHTML(fmt.Sprintf("%d. ", i+1), h)
		if utf8.RuneCountInString(text.String())+utf8.RuneCountInString(item) > notificationBudget {
			break
		}
		text.WriteString(item)
		shown++
	}
	if rest := totalFound - shown; rest > 0 {
		fmt.Fprintf(&text, "\n\n<i>И ещё %d терминов не показано. Открой LuxMed, чтобы посмотреть полный список.</i>", rest)
	}
	return text.String()
}

func historyHTML(items []domain.AppointmentHistory) string {
	lines := make([]string, 0, len(items))
	for _, h := range items {
		lines = append(lines, appointmentHTML("", h))
	}
	return strings.Join(lines, "\n\n")
}
