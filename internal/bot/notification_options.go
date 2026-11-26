package bot

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"slices"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

const (
	optionsFirst uint8 = iota
	optionsAfter
	optionsBefore
	optionsAtOrAfter
	optionsAtOrBefore
)

// Fixed-width fields keep even a hide action with a full cursor within Telegram's 64-byte limit.
type notificationOptionsAction struct {
	WatchID   int64
	MessageID int32
	Direction uint8
	Seconds   int64
	Nanos     uint32
	CursorID  int64
	HideID    int64
}

func (a notificationOptionsAction) callback() string {
	data, _ := binary.Append(nil, binary.BigEndian, a)
	return "options:" + base64.RawURLEncoding.EncodeToString(data)
}

func (a notificationOptionsAction) at(direction uint8, item domain.AppointmentHistory) notificationOptionsAction {
	a.Direction = direction
	a.Seconds = item.DateTime.Unix()
	a.Nanos = uint32(item.DateTime.Nanosecond())
	a.CursorID = item.ID
	return a
}

func parseNotificationOptions(data string) (notificationOptionsAction, bool) {
	var action notificationOptionsAction
	if parts := strings.Split(data, ":"); len(parts) == 3 {
		// Older buttons carry a numeric page. Reopen the batch from the start.
		watchID, watchErr := strconv.ParseInt(parts[0], 10, 64)
		page, pageErr := strconv.Atoi(parts[1])
		messageID, messageErr := strconv.ParseInt(parts[2], 10, 32)
		action.WatchID, action.MessageID = watchID, int32(messageID)
		return action, watchErr == nil && pageErr == nil && messageErr == nil &&
			page >= 0 && watchID > 0 && messageID > 0
	}
	wire, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil {
		return action, false
	}
	read, err := binary.Decode(wire, binary.BigEndian, &action)
	validCursor := (action.Direction == optionsFirst && action.CursorID == 0) ||
		(action.Direction > optionsFirst && action.Direction <= optionsAtOrBefore && action.CursorID > 0)
	return action, err == nil && read == len(wire) && action.WatchID > 0 && action.MessageID > 0 &&
		validCursor && action.Nanos < uint32(time.Second) && action.HideID >= 0
}

func (b *Bot) handleNotificationOptions(ctx context.Context, user domain.User, data string) {
	action, ok := parseNotificationOptions(data)
	if !ok {
		return
	}
	if action.HideID > 0 {
		if err := b.db.HideAppointmentHistory(ctx, user.ID, action.HideID); err != nil {
			b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
			return
		}
		action.HideID = 0
	}
	before := action.Direction == optionsBefore || action.Direction == optionsAtOrBefore
	cursor := storage.NotificationOptionsCursor{
		DateTime:  time.Unix(action.Seconds, int64(action.Nanos)),
		ID:        action.CursorID,
		Before:    before,
		Inclusive: action.Direction == optionsAtOrBefore || action.Direction == optionsAtOrAfter,
	}
	now := time.Now()
	items, err := b.db.AvailableNotificationOptions(
		ctx, user.ID, action.WatchID, int64(action.MessageID), now, historyPageSize+1, cursor,
	)
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), mainKeyboard(b.catalog, user.Locale))
		return
	}

	var navigation []tgbotapi.InlineKeyboardButton
	addPage := func(label string, target notificationOptionsAction) {
		navigation = append(navigation, tgbotapi.NewInlineKeyboardButtonData(
			b.catalog.T(user.Locale, label), target.callback(),
		))
	}
	if len(items) == 0 {
		// The opposite page can still contain the original boundary slot.
		switch {
		case action.Direction == optionsBefore:
			back := action
			back.Direction = optionsAtOrAfter
			addPage("button.next", back)
		case action.Direction == optionsAfter:
			back := action
			back.Direction = optionsAtOrBefore
			addPage("button.previous", back)
		}
	} else {
		more := len(items) > historyPageSize
		items = items[:min(len(items), historyPageSize)]
		if before {
			slices.Reverse(items)
		}
		if (before && more) || (!before && action.CursorID > 0) {
			addPage("button.previous", action.at(optionsBefore, items[0]))
		}
		if (!before && more) || before {
			addPage("button.next", action.at(optionsAfter, items[len(items)-1]))
		}
	}
	b.sendHistoryPage(user, items, now, "options.empty", navigation, func(id int64) string {
		hide := action
		hide.HideID = id
		return hide.callback()
	})
}
