package bot

import (
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
)

func mainKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.new_watch"), "new")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.my_watches"), "watches")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.history"), "history")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.account"), "account")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.settings"), "settings")),
	)
}

func settingsKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.language"), "settings:language")),
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

func doctorModeKeyboard(c *i18n.Catalog, locale string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.any_doctor"), "doctor:any")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.specific_doctor"), "doctor:exact")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.cancel"), "cancel")),
	)
}

func intervalKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("2 min", "interval:120"),
			tgbotapi.NewInlineKeyboardButtonData("3 min", "interval:180"),
			tgbotapi.NewInlineKeyboardButtonData("5 min", "interval:300"),
			tgbotapi.NewInlineKeyboardButtonData("10 min", "interval:600"),
		),
	)
}

func watchKeyboard(c *i18n.Catalog, locale string, watch domain.Watch) tgbotapi.InlineKeyboardMarkup {
	statusButton := tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.pause"), fmt.Sprintf("watch:pause:%d", watch.ID))
	if watch.Status == domain.WatchStatusPaused {
		statusButton = tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.resume"), fmt.Sprintf("watch:resume:%d", watch.ID))
	}
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(statusButton, tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.check_now"), fmt.Sprintf("watch:check:%d", watch.ID))),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.history"), "history"), tgbotapi.NewInlineKeyboardButtonData(c.T(locale, "button.delete"), fmt.Sprintf("watch:delete:%d", watch.ID))),
	)
}
