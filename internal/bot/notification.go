package bot

import (
	"cmp"
	"fmt"
	"html"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/procedures"
)

const (
	// A notification lists every slot up to this count; longer results show a preview.
	maxNotificationSlots     = 10
	previewNotificationSlots = 5
	// Telegram rejects messages over 4096 characters.
	notificationTextBudget = 3600
)

// appointmentNotification groups one search's slots by city, clinic and street,
// then by date, with times ascending. It returns the text and how many slots it shows.
func (b *Bot) appointmentNotification(locale string, watch domain.Watch, histories []domain.AppointmentHistory, total, limit int) (string, int) {
	count := max(total, len(histories))
	if count > maxNotificationSlots && limit > previewNotificationSlots {
		limit = previewNotificationSlots
	}
	title := b.catalog.T(locale, "notification.title", count)
	header := fmt.Sprintf("<b>%s</b>\n%s", html.EscapeString(title), html.EscapeString(truncateMessage(procedures.WatchTitle(watch), 240)))
	footer := "\n\n" + html.EscapeString(b.catalog.T(locale, "notification.booking_hint"))

	items := slices.Clone(histories[:min(limit, len(histories))])
	slices.SortStableFunc(items, compareSlotGroups)

	var text strings.Builder
	text.WriteString(header)
	budget := notificationTextBudget - utf8.RuneCountInString(header) - utf8.RuneCountInString(footer) - 120
	var shown int
	var prev *domain.AppointmentHistory
	for i := range items {
		item := &items[i]
		place := slotPlace(*item)
		newPlace := prev == nil || slotPlace(*prev) != place
		newDate := newPlace || dayOf(*prev) != dayOf(*item)
		var piece strings.Builder
		if newPlace {
			piece.WriteString("\n\n")
			if item.CityName != "" {
				fmt.Fprintf(&piece, "<b>%s</b>\n", html.EscapeString(truncateMessage(item.CityName, 60)))
			}
			if place.facility != "" {
				piece.WriteString(html.EscapeString(truncateMessage(place.facility, 120)))
			}
			if item.Address != "" {
				if place.facility != "" {
					piece.WriteString("\n")
				}
				piece.WriteString(html.EscapeString(truncateMessage(item.Address, 100)))
			}
		}
		if newDate {
			fmt.Fprintf(&piece, "\n<b>%s</b>", b.slotDate(locale, item.DateTime))
		}
		fmt.Fprintf(&piece, "\n%s", item.DateTime.In(luxmed.Location()).Format("15:04"))
		if item.DoctorName != "" {
			fmt.Fprintf(&piece, " · %s", html.EscapeString(truncateMessage(item.DoctorName, 80)))
		}
		if cost := utf8.RuneCountInString(piece.String()); cost > budget {
			break
		} else {
			budget -= cost
		}
		text.WriteString(piece.String())
		shown++
		prev = item
	}
	if remaining := count - shown; remaining > 0 {
		fmt.Fprintf(&text, "\n\n<i>%s</i>", html.EscapeString(b.catalog.T(locale, "notification.more", remaining)))
	}
	text.WriteString(footer)
	return text.String(), shown
}

type notificationPlace struct {
	city, facility, address string
}

func slotPlace(item domain.AppointmentHistory) notificationPlace {
	address := domain.NormalizeAppointmentAddress(item.Address)
	facility := item.FacilityName
	// LuxMed normalization can copy the address into an empty facility name.
	if address != "" && domain.NormalizeAppointmentAddress(facility) == address {
		facility = ""
	}
	return notificationPlace{city: item.CityName, facility: facility, address: address}
}

func dayOf(item domain.AppointmentHistory) string {
	return item.DateTime.In(luxmed.Location()).Format("02.01.2006")
}

func compareSlotGroups(a, b domain.AppointmentHistory) int {
	pa, pb := slotPlace(a), slotPlace(b)
	return cmp.Or(
		cmp.Compare(pa.city, pb.city),
		cmp.Compare(pa.facility, pb.facility),
		cmp.Compare(pa.address, pb.address),
		cmp.Compare(a.DateTime.In(luxmed.Location()).Format("20060102"), b.DateTime.In(luxmed.Location()).Format("20060102")),
		a.DateTime.Compare(b.DateTime),
		cmp.Compare(a.DoctorName, b.DoctorName),
		cmp.Compare(a.ID, b.ID),
	)
}
