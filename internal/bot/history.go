package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/procedures"
)

func (b *Bot) handleHistoryPage(ctx context.Context, user domain.User, data string) {
	parts := strings.Split(data, ":")
	var messageID int64
	switch {
	case len(parts) == 4 && parts[0] == "options":
		var err error
		messageID, err = strconv.ParseInt(parts[3], 10, 32)
		if err != nil || messageID <= 0 {
			return
		}
	case len(parts) == 3 && parts[0] == "history":
	default:
		return
	}
	watchID, idErr := strconv.ParseInt(parts[1], 10, 64)
	page, pageErr := strconv.Atoi(parts[2])
	validWatch := watchID > 0 || (parts[0] == "history" && watchID == 0)
	if idErr == nil && validWatch && pageErr == nil && page >= 0 && page <= 10000 {
		b.showHistoryPage(ctx, user, watchID, page, messageID)
	}
}

func (b *Bot) hideHistory(ctx context.Context, user domain.User, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 && len(parts) != 5 {
		return
	}
	view := "history"
	if len(parts) == 5 {
		messageID, err := strconv.ParseInt(parts[4], 10, 32)
		if err != nil || messageID <= 0 {
			return
		}
		view = "options"
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return
	}
	if err := b.db.HideAppointmentHistory(ctx, user.ID, id); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	b.handleHistoryPage(ctx, user, view+":"+strings.Join(parts[2:], ":"))
}

func (b *Bot) showHistoryPage(ctx context.Context, user domain.User, watchID int64, page int, messageID int64) {
	const pageSize = 5
	now := time.Now()
	var items []domain.AppointmentHistory
	var err error
	empty := "history.empty"
	view := "history"
	messageSuffix := ""
	if messageID > 0 {
		items, err = b.db.AvailableNotificationOptions(ctx, user.ID, watchID, messageID, now, pageSize+1, page*pageSize)
		empty = "options.empty"
		view = "options"
		messageSuffix = ":" + strconv.FormatInt(messageID, 10)
	} else {
		items, err = b.db.WatchHistory(ctx, user.ID, watchID, pageSize+1, page*pageSize)
	}
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	pageAction := func(page int) string {
		return fmt.Sprintf("%s:%d:%d%s", view, watchID, page, messageSuffix)
	}
	if len(items) == 0 {
		if page > 0 {
			b.send(user, b.catalog.T(user.Locale, empty), tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.previous"), pageAction(page-1))),
			))
			return
		}
		b.send(user, b.catalog.T(user.Locale, empty), mainKeyboard(b.catalog, user.Locale))
		return
	}
	var lines []string
	var navigation []tgbotapi.InlineKeyboardButton
	if page > 0 {
		navigation = append(navigation, tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.previous"), pageAction(page-1)))
	}
	if len(items) > pageSize {
		items = items[:pageSize]
		navigation = append(navigation, tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.next"), pageAction(page+1)))
	}
	var bookingURL string
	var hide []tgbotapi.InlineKeyboardButton
	for i, item := range items {
		line := fmt.Sprintf("%d. %s\n%s\n%s · %s", i+1,
			item.DateTime.In(luxmed.Location()).Format("2006-01-02 15:04"),
			truncateMessage(procedures.Name(user.Locale, item.ServiceName), 160), truncateMessage(item.DoctorName, 80), truncateMessage(item.FacilityName, 120))
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
			fmt.Sprintf("hide:%d:%d:%d%s", item.ID, watchID, page, messageSuffix),
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
