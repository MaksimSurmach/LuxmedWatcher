package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
)

func bookingCallback(data string) (string, int64) {
	action, raw, ok := strings.Cut(data, ":")
	if !ok || (action != "book" && action != "remind" && action != "unremind") {
		return "", 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return "", 0
	}
	return action, id
}

func (b *Bot) handleBookingAction(ctx context.Context, user domain.User, action string, id int64) string {
	if action != "book" {
		visit, err := b.db.SetBookingReminders(ctx, user.ID, id, action == "remind", time.Now())
		if err != nil {
			return b.catalog.T(user.Locale, "booking.unavailable")
		}
		key := "booking.reminders_off"
		if visit.Reminders {
			key = "booking.reminders_on"
		}
		text := b.catalog.T(user.Locale, key)
		// A separate native message also gives clients without widget support the inverse action.
		b.send(user, text, tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(b.reminderButton(user.Locale, visit).InlineKeyboardButton),
		))
		return text
	}
	if b.runner == nil {
		return b.catalog.T(user.Locale, "booking.portal")
	}
	select {
	case b.manualChecks <- struct{}{}:
	default:
		return b.catalog.T(user.Locale, "booking.busy")
	}
	b.workers.Go(func() {
		defer func() { <-b.manualChecks }()
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		visit, err := b.runner.BookSlot(ctx, user, id)
		key := ""
		switch {
		case err == nil && visit.Status == "pending":
			key = "booking.unknown"
		case err == nil:
			// The scheduler sends the durable confirmation or unknown outcome.
			return
		case errors.Is(err, luxmed.ErrSlotTaken) || errors.Is(err, sql.ErrNoRows):
			key = "booking.unavailable"
		case errors.Is(err, luxmed.ErrBookingInPortal):
			key = "booking.portal"
		case errors.Is(err, luxmed.ErrBookingUnknown):
			key = "booking.unknown"
		case errors.Is(err, luxmed.ErrBrowserBusy):
			key = "booking.busy"
		case errors.Is(err, luxmed.ErrAuthentication) || errors.Is(err, scheduler.ErrNoAccount):
			key = "booking.sign_in"
		default:
			key = "errors.generic"
		}
		var markup any
		if key == "booking.portal" || key == "booking.unknown" {
			markup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonURL(b.catalog.T(user.Locale, "button.book_portal"), luxmed.ReservationPageURL),
			))
		}
		b.send(user, b.catalog.T(user.Locale, key), markup)
	})
	return b.catalog.T(user.Locale, "booking.working")
}

func (b *Bot) reminderButton(locale string, visit domain.Booking) miniAppButton {
	key, action := "booking.enable", "remind"
	if visit.Reminders {
		key, action = "booking.disable", "unremind"
	}
	return b.actionButton(domain.User{Locale: locale}, key, fmt.Sprintf("%s:%d", action, visit.ID))
}

func (b *Bot) NotifyBooking(ctx context.Context, user domain.User, visit domain.Booking, kind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if user.ID != visit.UserID || !user.IsActive() || user.TelegramUserID <= 0 || user.TelegramChatID != user.TelegramUserID {
		return errors.New("booking recipient has no private chat")
	}
	key := "booking." + kind
	caption := "<b>" + html.EscapeString(b.catalog.T(user.Locale, key)) + "</b>"
	text := caption + "\n" + html.EscapeString(b.slotDateTime(user.Locale, visit.DateTime))
	for _, field := range []string{visit.Service, visit.Doctor, visit.Place} {
		if field != "" {
			text += "\n" + html.EscapeString(truncateMessage(field, 200))
		}
	}
	var actions [][]miniAppButton
	msg := tgbotapi.NewMessage(user.TelegramChatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.DisableWebPagePreview = true
	switch kind {
	case "confirmed":
		actions = [][]miniAppButton{{b.reminderButton(user.Locale, visit)}}
		if visit.Automatic {
			msg.Text += "\n\n" + html.EscapeString(b.catalog.T(user.Locale, "booking.watch_stopped"))
		}
	case "unknown":
		actions = [][]miniAppButton{{{InlineKeyboardButton: tgbotapi.NewInlineKeyboardButtonURL(
			b.catalog.T(user.Locale, "button.book_portal"), luxmed.ReservationPageURL)}}}
	}
	if len(actions) > 0 {
		msg.ReplyMarkup = map[string][][]miniAppButton{"inline_keyboard": actions}
	}

	card := b.newWidgetCard(user.Locale, actions)
	card.Slots = []widgetSlot{b.newWidgetSlot(user, domain.AppointmentHistory{Appointment: domain.Appointment{
		DateTime: visit.DateTime, DoctorName: visit.Doctor, Address: visit.Place,
	}})}
	card.Slots[0].URL = ""
	card.Slots[0].Label = strings.Join([]string{card.Slots[0].Label, visit.Service}, " · ")
	card.Slots[0].Doctor = truncateMessage(strings.Trim(strings.Join([]string{visit.Doctor, visit.Service}, " · "), " ·"), 110)
	if visit.Automatic && kind == "confirmed" {
		card.BookingHint = b.catalog.T(user.Locale, "booking.watch_stopped")
	}
	if _, err := b.sendWidget(card, caption, msg); err != nil {
		return errors.New("telegram booking message delivery failed")
	}
	return nil
}
