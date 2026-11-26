package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

const historyPageSize = 5

func (b *Bot) handleHistoryPage(ctx context.Context, user domain.User, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return
	}
	watchID, idErr := strconv.ParseInt(parts[1], 10, 64)
	page, pageErr := strconv.Atoi(parts[2])
	if idErr == nil && watchID >= 0 && pageErr == nil && page >= 0 && page <= 10000 {
		b.showHistoryPage(ctx, user, watchID, page)
	}
}

func (b *Bot) hideHistory(ctx context.Context, user domain.User, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 && len(parts) != 5 {
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return
	}
	if err := b.db.HideAppointmentHistory(ctx, user.ID, id); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	if len(parts) == 5 {
		// Legacy notification hide buttons used a numeric page.
		b.handleNotificationOptions(ctx, user, strings.Join(parts[2:], ":"))
		return
	}
	b.handleHistoryPage(ctx, user, "history:"+strings.Join(parts[2:], ":"))
}

func (b *Bot) showHistoryPage(ctx context.Context, user domain.User, watchID int64, page int) {
	now := time.Now()
	items, err := b.db.WatchHistory(ctx, user.ID, watchID, historyPageSize+1, page*historyPageSize)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	var navigation []tgbotapi.InlineKeyboardButton
	if page > 0 {
		navigation = append(navigation, tgbotapi.NewInlineKeyboardButtonData(
			b.catalog.T(user.Locale, "button.previous"), fmt.Sprintf("history:%d:%d", watchID, page-1),
		))
	}
	if len(items) > historyPageSize {
		items = items[:historyPageSize]
		navigation = append(navigation, tgbotapi.NewInlineKeyboardButtonData(
			b.catalog.T(user.Locale, "button.next"), fmt.Sprintf("history:%d:%d", watchID, page+1),
		))
	}
	b.sendHistoryPage(user, items, now, "history.empty", navigation, func(id int64) string {
		return fmt.Sprintf("hide:%d:%d:%d", id, watchID, page)
	})
}

func (b *Bot) sendHistoryPage(
	user domain.User, items []domain.AppointmentHistory, now time.Time, empty string,
	navigation []tgbotapi.InlineKeyboardButton, hideAction func(int64) string,
) {
	if len(items) == 0 {
		markup := mainKeyboard(b.catalog, user.Locale)
		if len(navigation) > 0 {
			markup = tgbotapi.NewInlineKeyboardMarkup(navigation)
		}
		b.send(user, b.catalog.T(user.Locale, empty), markup)
		return
	}
	var lines []string
	var bookingURL string
	var hide []tgbotapi.InlineKeyboardButton
	for i, item := range items {
		line := fmt.Sprintf("%d. %s\n%s\n%s · %s", i+1,
			appointmentDateTime(user.Locale, item.DateTime),
			truncateMessage(item.ServiceName, 160), truncateMessage(item.DoctorName, 80), truncateMessage(item.FacilityName, 120))
		if item.Address != "" {
			line += "\n" + truncateMessage(item.Address, 100)
		}
		switch {
		case !item.DateTime.After(now):
			line += "\n" + b.catalog.T(user.Locale, "history.expired")
		case item.Status == domain.HistoryStatusExpired:
			line += "\n" + b.catalog.T(user.Locale, "history.unavailable")
		case bookingURL == "":
			bookingURL = portalURL(item.BookingURL)
		}
		if item.LastNotifiedAt != nil {
			line += "\n" + b.catalog.T(user.Locale, "history.notified")
		}
		lines = append(lines, line)
		hide = append(hide, tgbotapi.NewInlineKeyboardButtonData(
			b.catalog.T(user.Locale, "button.hide_history", i+1),
			hideAction(item.ID),
		))
	}
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, 4)
	if bookingURL != "" {
		lines = append(lines, b.catalog.T(user.Locale, "notification.booking_hint"))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.book_portal"), bookingURL),
		))
	}
	rows = append(rows, hide)
	if len(navigation) > 0 {
		rows = append(rows, navigation)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.menu"), "menu")))
	b.send(user, strings.Join(lines, "\n\n"), tgbotapi.NewInlineKeyboardMarkup(rows...))
}
