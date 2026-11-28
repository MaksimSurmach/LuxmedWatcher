package bot

import (
	"strings"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

func appointmentDate(locale string, value time.Time) string {
	layout := "02.01.2006"
	if i18n.Normalize(locale, "pl") == "en" {
		layout = "Jan 2, 2006"
	}
	return value.In(luxmed.Location()).Format(layout)
}

func appointmentDateTime(locale string, value time.Time) string {
	return appointmentDate(locale, value) + " · " + value.In(luxmed.Location()).Format("15:04")
}

// slotDate uses the clinic's calendar day, including around UTC midnight.
func (b *Bot) slotDate(locale string, value time.Time) string {
	local := value.In(luxmed.Location())
	weekdays := strings.Split(b.catalog.T(locale, "slot.weekdays"), ",")
	return weekdays[local.Weekday()] + " " + local.Format("02.01")
}

func (b *Bot) slotDateTime(locale string, value time.Time) string {
	return b.slotDate(locale, value) + " " + value.In(luxmed.Location()).Format("15:04")
}

func (b *Bot) intervalText(locale string, value time.Duration) string {
	minutes := int(value / time.Minute)
	seconds := int(value % time.Minute / time.Second)
	if seconds == 0 {
		return b.catalog.T(locale, "button.interval_minutes", minutes)
	}
	remainder := b.catalog.T(locale, "button.interval_seconds", seconds)
	if minutes == 0 {
		return remainder
	}
	return b.catalog.T(locale, "button.interval_minutes", minutes) + " " + remainder
}
