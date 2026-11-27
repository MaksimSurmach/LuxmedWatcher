package bot

import (
	"fmt"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
)

func mainKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.new_watch"), "new")),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.my_watches"), "watches"),
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.history"), "history"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.account"), "account"),
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.settings"), "settings"),
		),
	)
}

func settingsKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.language"), "settings:language")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.city"), "settings:city")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.menu"), "menu")),
	)
}

func languageKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("English", "lang:en"),
			tgbotapi.NewInlineKeyboardButtonData("Русский", "lang:ru"),
			tgbotapi.NewInlineKeyboardButtonData("Polski", "lang:pl"),
		),
	)
}

func cancelKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)
}

// intervalKeyboard offers only intervals the server will honour, so nothing is silently raised.
func intervalKeyboard(c *i18n.Catalog, locale string, minimum time.Duration) tgbotapi.InlineKeyboardMarkup {
	var row []tgbotapi.InlineKeyboardButton
	for _, minutes := range []int{2, 3, 5, 10} {
		if time.Duration(minutes)*time.Minute >= minimum {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.interval_minutes", minutes), fmt.Sprintf("interval:%d", minutes*60)))
		}
	}
	if len(row) == 0 {
		minutes := int(minimum.Minutes())
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.interval_minutes", minutes), fmt.Sprintf("interval:%d", int(minimum.Seconds()))))
	}
	return tgbotapi.NewInlineKeyboardMarkup(row)
}

func timeWindowKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.time_any"), "time:any")),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.time_morning"), "time:morning"),
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.time_day"), "time:day"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.time_evening"), "time:evening"),
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.time_workday"), "time:workday"),
		),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)
}

func watchKeyboard(c *i18n.Catalog, locale string, watch domain.Watch) tgbotapi.InlineKeyboardMarkup {
	statusButton := tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.pause"), fmt.Sprintf("watch:pause:%d", watch.ID))
	if watch.Status == domain.WatchStatusPaused {
		statusButton = tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.resume"), fmt.Sprintf("watch:resume:%d", watch.ID))
	}
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(statusButton, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.check_now"), fmt.Sprintf("watch:check:%d", watch.ID))),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.delete"), fmt.Sprintf("watch:delete:%d", watch.ID))),
	)
}

func cityKeyboard(cities []domain.City, prefix string, page int, hasPrevious bool, hasNext bool, c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	for i := 0; i < len(cities); i += 2 {
		row := []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData(cities[i].Name, fmt.Sprintf("%s%d", prefix, cities[i].ID)),
		}
		if i+1 < len(cities) {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(cities[i+1].Name, fmt.Sprintf("%s%d", prefix, cities[i+1].ID)))
		}
		rows = append(rows, row)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.search_city"), prefix+"search")))
	var nav []tgbotapi.InlineKeyboardButton
	if hasPrevious {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.previous"), fmt.Sprintf("%spage:%d", prefix, page-1)))
	}
	if hasNext {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.next"), fmt.Sprintf("%spage:%d", prefix, page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func citySearchKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)
}

func procedureMenuKeyboard(_ []domain.Procedure, c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.popular_procedures"), "proc:popular")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.search"), "proc:search")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.show_all_procedures"), "proc:all")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)
}

func procedureLettersKeyboard(letters []string, c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton

	for i := 0; i < len(letters); i += 5 {
		var row []tgbotapi.InlineKeyboardButton
		for j := i; j < len(letters) && j < i+5; j++ {
			letter := letters[j]
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(letter, fmt.Sprintf("procletter:%s:0", letter)))
		}
		rows = append(rows, row)
	}

	rows = append(rows,
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.search"), "proc:search")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func procedureResultsKeyboard(procedures []domain.Procedure, previousCallback string, nextCallback string, c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton

	for i := 0; i < len(procedures); i += 5 {
		var row []tgbotapi.InlineKeyboardButton
		for j := i; j < len(procedures) && j < i+5; j++ {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%d", j+1), fmt.Sprintf("proc:%d", procedures[j].ID)))
		}
		rows = append(rows, row)
	}

	var nav []tgbotapi.InlineKeyboardButton
	if previousCallback != "" {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.previous"), previousCallback))
	}
	if nextCallback != "" {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.next"), nextCallback))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}

	rows = append(rows,
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.popular_procedures"), "proc:popular")),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.search"), "proc:search"),
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.show_all_procedures"), "proc:all"),
		),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func procedureSearchKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.show_all_procedures"), "proc:all")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)
}

func facilityMenuKeyboard(_ []domain.Facility, c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.all_places_in_city"), "fac:any")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.show_all_favorite_places"), "fac:favs")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.show_all_places"), "fac:all")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)
}

func facilityResultsKeyboard(facilities []domain.Facility, mode string, page int, previousCallback string, nextCallback string, selectedIDs []int, c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton

	for i := 0; i < len(facilities); i += 5 {
		var row []tgbotapi.InlineKeyboardButton

		for j := i; j < len(facilities) && j < i+5; j++ {
			label := fmt.Sprintf("%d", j+1)
			if facilityIDSelected(selectedIDs, facilities[j].ID) {
				label = "✅ " + label
			}

			row = append(row, tgbotapi.NewInlineKeyboardButtonData(
				label,
				fmt.Sprintf("facsel:%s:%d:%d", mode, page, facilities[j].ID),
			))
		}

		rows = append(rows, row)
	}

	var nav []tgbotapi.InlineKeyboardButton
	if previousCallback != "" {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.previous"), previousCallback))
	}
	if nextCallback != "" {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.next"), nextCallback))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}

	rows = append(rows,
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.done"), "facdone"),
			tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.clear"), "facclear"),
		),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.all_places_in_city"), "fac:any")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}
