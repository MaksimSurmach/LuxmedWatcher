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

// Watch intervals are persisted as whole seconds. Rounding up preserves the configured minimum.
func intervalSeconds(value time.Duration) int {
	seconds := int(value / time.Second)
	if value%time.Second > 0 {
		seconds++
	}
	return seconds
}

func (b *Bot) intervalText(locale string, totalSeconds int) string {
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	if seconds == 0 {
		return b.catalog.T(locale, "button.interval_minutes", minutes)
	}
	remainder := b.catalog.T(locale, "button.interval_seconds", seconds)
	if minutes == 0 {
		return remainder
	}
	return b.catalog.T(locale, "button.interval_minutes", minutes) + " " + remainder
}
