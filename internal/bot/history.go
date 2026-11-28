package bot

import (
	"context"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
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
	var card widgetCard
	var hide []tgbotapi.InlineKeyboardButton
	for i, item := range items {
		when := b.slotDateTime(user.Locale, item.DateTime)
		if item.DateTime.Before(now) && item.DateTime.In(luxmed.Location()).Year() != now.In(luxmed.Location()).Year() {
			when = appointmentDateTime(user.Locale, item.DateTime)
		}
		var details []string
		if item.DoctorName != "" {
			details = append(details, truncateMessage(item.DoctorName, 80))
		}
		if facility := slotPlace(item).facility; facility != "" {
			details = append(details, truncateMessage(facility, 120))
		}
		line := fmt.Sprintf("%d. %s\n%s", i+1, when, truncateMessage(item.ServiceName, 160))
		if len(details) > 0 {
			line += "\n" + strings.Join(details, " · ")
		}
		if item.Address != "" {
			line += "\n" + truncateMessage(item.Address, 100)
		}
		coupon := b.newWidgetSlot(user.Locale, item)
		coupon.Hide, coupon.HideLabel = hideAction(item.ID), b.catalog.T(user.Locale, "button.hide")
		if item.ServiceName != "" {
			coupon.Doctor = strings.Join(slices.DeleteFunc([]string{coupon.Doctor, truncateMessage(item.ServiceName, 64)}, func(s string) bool { return s == "" }), " · ")
		}
		switch {
		case !item.DateTime.After(now):
			line += "\n" + b.catalog.T(user.Locale, "history.expired")
			coupon.Stamp = b.catalog.T(user.Locale, "history.stamp_past")
		case item.Status == domain.HistoryStatusExpired:
			line += "\n" + b.catalog.T(user.Locale, "history.unavailable")
			coupon.Stamp = b.catalog.T(user.Locale, "history.stamp_gone")
		case bookingURL == "":
			bookingURL = portalURL(item.BookingURL)
		}
		card.Slots = append(card.Slots, coupon)
		if item.LastNotifiedAt != nil {
			line += "\n" + b.catalog.T(user.Locale, "history.notified")
		}
		lines = append(lines, line)
		hide = append(hide, tgbotapi.NewInlineKeyboardButtonData(
			b.catalog.T(user.Locale, "button.hide_history", i+1),
			hideAction(item.ID),
		))
	}
	// Coupons carry their own booking and hide buttons, so the card keeps paging and the menu in one
	// row and stays under Telegram's 600px widget height; the text fallback keeps every row.
	var top, bottom [][]tgbotapi.InlineKeyboardButton
	if bookingURL != "" {
		lines = append(lines, b.catalog.T(user.Locale, "notification.booking_hint"))
		top = append(top, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.book_portal"), bookingURL),
		))
	}
	if len(navigation) > 0 {
		bottom = append(bottom, navigation)
	}
	bottom = append(bottom, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.menu"), "menu")))
	rows := slices.Concat(top, [][]tgbotapi.InlineKeyboardButton{hide}, bottom)
	title := b.catalog.T(user.Locale, "button.history")
	if empty == "options.empty" {
		title = b.catalog.T(user.Locale, "button.all_options")
	}
	slots := card.Slots
	card = b.newWidgetCard(user.Locale, widgetKeyboard(tgbotapi.NewInlineKeyboardMarkup(slices.Concat(bottom...))))
	card.Slots = slots
	fallback := tgbotapi.NewMessage(user.TelegramChatID, html.EscapeString(truncateMessage(strings.Join(lines, "\n\n"), 4000)))
	fallback.ParseMode = tgbotapi.ModeHTML
	fallback.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	if _, err := b.sendWidget(card, "<b>"+html.EscapeString(title)+"</b>", fallback); err != nil {
		b.logger.Warn("send Telegram history page failed")
	}
}
