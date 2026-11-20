package bot

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

func allowsFlowCallback(state *flow, data string) bool {
	kind := flowNone
	settingsCity := false
	if state != nil {
		kind, settingsCity = state.Kind, state.CitySettings
	}
	switch {
	case strings.HasPrefix(data, "prefcity:"):
		return (kind == flowWatchCity || kind == flowCitySearch) && settingsCity
	case strings.HasPrefix(data, "city:"):
		return (kind == flowWatchCity || kind == flowCitySearch) && !settingsCity
	case strings.HasPrefix(data, "proc:") || strings.HasPrefix(data, "procletter:") || strings.HasPrefix(data, "procsearchpage:"):
		return kind == flowWatchService || kind == flowWatchSearch
	case strings.HasPrefix(data, "doctor:"):
		return kind == flowWatchDoctor || kind == flowDoctorSearch
	case strings.HasPrefix(data, "fac"):
		return kind == flowWatchFacility
	case strings.HasPrefix(data, "date:"):
		return kind == flowWatchDate
	case strings.HasPrefix(data, "time:"):
		return kind == flowWatchTime
	case strings.HasPrefix(data, "days:"):
		return kind == flowWatchDays
	case strings.HasPrefix(data, "interval:"):
		return kind == flowWatchInterval
	case strings.HasPrefix(data, "confirm:"):
		return kind == flowWatchConfirm
	default:
		return true
	}
}

func (b *Bot) handleDoctorChoice(ctx context.Context, user domain.User, state *flow, choice string) {
	if choice == "any" {
		state.Watch.DoctorMode, state.Watch.DoctorID, state.Watch.DoctorName = domain.DoctorModeAny, nil, ""
		b.showFacilityMenu(ctx, user, state)
		return
	}
	if choice == "exact" {
		if state.Doctors == nil {
			client, err := b.authenticatedLuxMedClient(ctx, user)
			if err == nil {
				var directory domain.DoctorsAndFacilities
				directory, err = client.GetDoctorsAndFacilities(ctx, state.Watch.CityID, state.Watch.ServiceID)
				state.Doctors = directory.Doctors
			}
			if err != nil {
				b.send(user, b.catalog.T(user.Locale, "errors.generic"), doctorModeKeyboard(b.catalog, user.Locale))
				return
			}
		}
		state.Kind = flowDoctorSearch
		b.showDoctorResults(user, state, "")
		return
	}
	id, err := strconv.Atoi(choice)
	if err != nil || id <= 0 || state.Kind != flowDoctorSearch {
		return
	}
	for _, doctor := range state.Doctors {
		if doctor.ID == id {
			state.Watch.DoctorMode, state.Watch.DoctorID, state.Watch.DoctorName = domain.DoctorModeExact, &id, doctor.DisplayName()
			b.showFacilityMenu(ctx, user, state)
			return
		}
	}
}

func (b *Bot) showDoctorResults(user domain.User, state *flow, query string) {
	var rows [][]tgbotapi.InlineKeyboardButton
	query = strings.ToLower(strings.TrimSpace(query))
	for _, doctor := range state.Doctors {
		name := strings.TrimSpace(doctor.DisplayName())
		if doctor.ID <= 0 || name == "" || !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(truncateMessage(name, 64), fmt.Sprintf("doctor:%d", doctor.ID))))
		if len(rows) == 8 {
			break
		}
	}
	key := "flow.watch.doctor_exact"
	if len(rows) == 0 {
		key = "flow.watch.doctor_not_found"
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.any_doctor"), "doctor:any"),
		tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.cancel"), "cancel"),
	))
	b.send(user, b.catalog.T(user.Locale, key), tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (b *Bot) showDateStep(user domain.User, state *flow) {
	state.Kind = flowWatchDate
	b.setFlow(user.ID, state)
	var choices []tgbotapi.InlineKeyboardButton
	for _, days := range []int{7, 14, 30} {
		choices = append(choices, tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.next_days", days), fmt.Sprintf("date:%d", days)))
	}
	b.send(user, b.catalog.T(user.Locale, "flow.watch.date"), tgbotapi.NewInlineKeyboardMarkup(
		choices, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.cancel"), "cancel")),
	))
}

func (b *Bot) handleWatchDate(user domain.User, state *flow, input string) {
	if n, err := strconv.Atoi(input); err == nil && n >= 1 && n <= 90 {
		state.Watch.NextDays, state.Watch.DateFrom, state.Watch.DateTo = n, nil, nil
		b.showTimeStep(user, state)
		return
	}
	start, end, ok := parseWatchDates(input, time.Now())
	if !ok {
		b.showDateStep(user, state)
		return
	}
	state.Watch.NextDays, state.Watch.DateFrom, state.Watch.DateTo = 0, &start, &end
	b.showTimeStep(user, state)
}

func parseWatchDates(input string, now time.Time) (time.Time, time.Time, bool) {
	fields := strings.Fields(input)
	if len(fields) != 2 {
		return time.Time{}, time.Time{}, false
	}
	location := luxmed.Location()
	start, startErr := time.ParseInLocation(time.DateOnly, fields[0], location)
	end, endErr := time.ParseInLocation(time.DateOnly, fields[1], location)
	now = now.In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	return start, end, startErr == nil && endErr == nil && !start.Before(today) && !end.Before(start) && !end.After(today.AddDate(0, 0, 90))
}

func (b *Bot) showDaysStep(user domain.User, state *flow) {
	state.Kind = flowWatchDays
	b.send(user, b.catalog.T(user.Locale, "flow.watch.days"), tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.any_day"), "days:any"),
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.weekdays"), "days:weekdays"),
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.weekend"), "days:weekend"),
		),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.cancel"), "cancel")),
	))
}

func (b *Bot) selectWatchDays(user domain.User, state *flow, input string) {
	input = strings.TrimSpace(input)
	switch input {
	case "any":
		b.showIntervalStep(user, state)
		return
	case "weekdays":
		input = "1,2,3,4,5"
	case "weekend":
		input = "6,7"
	}
	window := domain.TimeWindow{From: "00:00", To: "23:59"}
	if len(state.Watch.TimeWindows) > 0 {
		window = state.Watch.TimeWindows[0]
	}
	var windows []domain.TimeWindow
	for _, token := range strings.Split(input, ",") {
		day, err := strconv.Atoi(strings.TrimSpace(token))
		if err != nil || day < 1 || day > 7 || slices.ContainsFunc(windows, func(w domain.TimeWindow) bool { return w.Weekday == day }) {
			b.showDaysStep(user, state)
			return
		}
		window.Weekday = day
		windows = append(windows, window)
	}
	slices.SortFunc(windows, func(a, b domain.TimeWindow) int { return a.Weekday - b.Weekday })
	state.Watch.TimeWindows = windows
	b.showIntervalStep(user, state)
}

func (b *Bot) showIntervalStep(user domain.User, state *flow) {
	state.Kind = flowWatchInterval
	interval := max(b.cfg.DefaultInterval, b.cfg.MinimumInterval, time.Minute)
	var choices []tgbotapi.InlineKeyboardButton
	for _, minutes := range []int{2, 5, 10} {
		if time.Duration(minutes)*time.Minute >= b.cfg.MinimumInterval {
			choices = append(choices, tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.interval_minutes", minutes), fmt.Sprintf("interval:%d", minutes)))
		}
	}
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.interval_default", interval.String()), "interval:default")),
	}
	if len(choices) > 0 {
		rows = append(rows, choices)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.cancel"), "cancel")))
	b.send(user, b.catalog.T(user.Locale, "flow.watch.interval", b.cfg.MinimumInterval.String()), tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (b *Bot) previewWatch(user domain.User, state *flow, interval time.Duration) {
	if interval < b.cfg.MinimumInterval {
		b.showIntervalStep(user, state)
		return
	}
	state.Watch.CheckIntervalSeconds = int(max(time.Minute, interval).Seconds())
	state.Watch.Name = state.Watch.ServiceName + " · " + state.Watch.CityName
	state.Kind, state.Confirmation = flowWatchConfirm, randomCode()
	b.send(user, b.watchSummary(user, state.Watch), tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.confirm_watch"), "confirm:"+state.Confirmation),
			tgbotapi.NewInlineKeyboardButtonData(b.catalog.T(user.Locale, "button.cancel"), "cancel"),
		),
	))
}

func (b *Bot) watchSummary(user domain.User, watch domain.Watch) string {
	tr := func(key string, args ...any) string { return b.catalog.T(user.Locale, key, args...) }
	doctor := watch.DoctorName
	if doctor == "" {
		doctor = tr("button.any_doctor")
	}
	facilities := strings.Join(watch.FacilityNames, ", ")
	if facilities == "" {
		facilities = tr("button.all_places_in_city")
	}
	dates := tr("button.next_days", watch.NextDays)
	if watch.DateFrom != nil && watch.DateTo != nil {
		dates = watch.DateFrom.In(luxmed.Location()).Format(time.DateOnly) + " – " + watch.DateTo.In(luxmed.Location()).Format(time.DateOnly)
	}
	hours, days := tr("button.any_time"), tr("button.any_day")
	if len(watch.TimeWindows) > 0 {
		window := watch.TimeWindows[0]
		if window.From != "00:00" || window.To != "23:59" {
			hours = window.From + "–" + window.To
		}
		if window.Weekday != 0 {
			weekdays := strings.Split(tr("calendar.weekdays"), ",")
			var names []string
			for _, window := range watch.TimeWindows {
				if window.Weekday > 0 && window.Weekday <= len(weekdays) {
					names = append(names, weekdays[window.Weekday-1])
				}
			}
			days = strings.Join(names, ", ")
		}
	}
	return tr("flow.watch.preview", watch.ServiceName, watch.CityName, doctor, truncateMessage(facilities, 400), dates, days, hours, (time.Duration(watch.CheckIntervalSeconds) * time.Second).String())
}

func (b *Bot) finishWatch(ctx context.Context, user domain.User, token string) {
	state := b.getFlow(user.ID)
	if state == nil || state.Kind != flowWatchConfirm || token == "" || token != state.Confirmation {
		return
	}
	watch := state.Watch
	if watch.UserID != user.ID || watch.CityID <= 0 || watch.ServiceID <= 0 {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
		return
	}
	if _, err := b.db.LuxMedAccount(ctx, user.ID); err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.no_account"), mainKeyboard(b.catalog, user.Locale))
		return
	}
	key := "flow.watch.created"
	var err error
	if watch.ID == 0 {
		_, err = b.db.CreateWatch(ctx, watch)
	} else {
		key = "flow.watch.updated"
		err = b.db.UpdateWatch(ctx, watch)
	}
	if err != nil {
		b.send(user, b.catalog.T(user.Locale, "errors.generic"), nil)
		return
	}
	b.clearFlow(user.ID)
	b.send(user, b.catalog.T(user.Locale, key), mainKeyboard(b.catalog, user.Locale))
}

func (b *Bot) editWatch(ctx context.Context, user domain.User, watch domain.Watch) {
	state := &flow{Kind: flowWatchCity, Watch: watch}
	b.setFlow(user.ID, state)
	b.showCityPicker(ctx, user, false, 0)
}
