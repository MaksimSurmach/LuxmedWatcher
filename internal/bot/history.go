package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

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

func (b *Bot) showHistoryPage(ctx context.Context, user domain.User, watchID int64, page int) {
	const pageSize = 5
	var items []domain.AppointmentHistory
	var err error
	if watchID == 0 {
		items, err = b.db.RecentHistory(ctx, user.ID, pageSize+1, page*pageSize)
	} else {
		items, err = b.db.WatchHistory(ctx, user.ID, watchID, pageSize+1, page*pageSize)
	}
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	if len(items) == 0 {
		b.send(user, b.catalog.T(user.Locale, "history.empty"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	var lines []string
	var navigation []tgbotapi.InlineKeyboardButton
	if page > 0 {
		navigation = append(navigation, tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.previous"), fmt.Sprintf("history:%d:%d", watchID, page-1)))
	}
	if len(items) > pageSize {
		items = items[:pageSize]
		navigation = append(navigation, tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.next"), fmt.Sprintf("history:%d:%d", watchID, page+1)))
	}
	for _, item := range items {
		line := fmt.Sprintf("%s\n%s\n%s · %s",
			item.DateTime.In(luxmed.Location()).Format("2006-01-02 15:04"),
			truncateMessage(item.ServiceName, 160), truncateMessage(item.DoctorName, 80), truncateMessage(item.FacilityName, 120))
		if item.LastNotifiedAt != nil {
			line += "\n" + b.catalog.T(user.Locale, "history.notified")
		}
		lines = append(lines, line)
	}
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.open_luxmed"), portalURL(items[0].BookingURL))),
	}
	if len(navigation) > 0 {
		rows = append(rows, navigation)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.menu"), "menu")))
	b.send(user, strings.Join(lines, "\n\n"), tgbotapi.NewInlineKeyboardMarkup(rows...))
}
