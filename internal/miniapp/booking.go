package miniapp

import (
	"errors"
	"net/http"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
)

type bookingInput struct {
	ID      int64
	Enabled bool
}

func readBookingInput(w http.ResponseWriter, r *http.Request) (bookingInput, bool) {
	var input bookingInput
	if !decodeDashboard(w, r, &input) {
		return input, false
	}
	if input.ID <= 0 {
		reply(w, http.StatusBadRequest, nil)
		return input, false
	}
	return input, true
}

func (d *dashboard) book(w http.ResponseWriter, r *http.Request, user domain.User) {
	input, ok := readBookingInput(w, r)
	if !ok {
		return
	}
	visit, err := d.runner.BookSlot(r.Context(), user, input.ID)
	switch {
	case errors.Is(err, luxmed.ErrSlotTaken):
		reply(w, http.StatusOK, map[string]string{"Status": "taken"})
	case errors.Is(err, luxmed.ErrBookingInPortal):
		reply(w, http.StatusOK, map[string]string{"Status": "portal"})
	case errors.Is(err, luxmed.ErrBookingUnknown):
		reply(w, http.StatusOK, map[string]string{"Status": "unknown"})
	case errors.Is(err, scheduler.ErrNoAccount):
		dashboardResult(w, nil, luxmed.ErrAuthentication)
	default:
		dashboardResult(w, visit, err)
	}
}

func (d *dashboard) autoBook(w http.ResponseWriter, r *http.Request, user domain.User) {
	input, ok := readBookingInput(w, r)
	if ok {
		dashboardResult(w, nil, d.db.SetWatchAutoBook(r.Context(), user.ID, input.ID, input.Enabled))
	}
}

func (d *dashboard) reminders(w http.ResponseWriter, r *http.Request, user domain.User) {
	input, ok := readBookingInput(w, r)
	if ok {
		visit, err := d.db.SetBookingReminders(r.Context(), user.ID, input.ID, input.Enabled, time.Now())
		dashboardResult(w, visit, err)
	}
}
