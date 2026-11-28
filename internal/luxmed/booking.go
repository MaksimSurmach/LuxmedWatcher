package luxmed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

const (
	lockTermURL    = "/PatientPortal/NewPortal/Reservation/LockTerm"
	confirmTermURL = "/PatientPortal/NewPortal/Reservation/Confirm"
	releaseTermURL = "/PatientPortal/NewPortal/Reservation/ReleaseTerm"
)

var (
	ErrSlotTaken       = errors.New("luxmed slot is no longer available")
	ErrBookingInPortal = errors.New("luxmed requires the patient portal")
	ErrBookingUnknown  = errors.New("luxmed booking outcome is unknown")
)

type bookingTerm struct {
	CityID        int       `json:"cityId"`
	CityName      string    `json:"cityName"`
	ServiceID     int       `json:"serviceVariantId"`
	ServiceName   string    `json:"serviceVariantName"`
	FacilityID    int       `json:"facilityId"`
	FacilityName  string    `json:"facilityName"`
	RoomID        int       `json:"roomId"`
	ScheduleID    int       `json:"scheduleId"`
	DoctorID      int       `json:"doctorId"`
	Doctor        any       `json:"doctor"`
	Date          time.Time `json:"date"`
	TimeFrom      string    `json:"timeFrom"`
	TimeTo        string    `json:"timeTo"`
	CorrelationID string    `json:"correlationId,omitempty"`
}

type bookingReply struct {
	Errors   *bool `json:"hasErrors"`
	Warnings bool  `json:"hasWarnings"`
	Issues   []struct {
		Code int `json:"code"`
	} `json:"errors"`
	Value json.RawMessage `json:"value"`
}

func (reply bookingReply) rejection() error {
	for _, issue := range reply.Issues {
		if issue.Code == 11000083 {
			return ErrSlotTaken
		}
	}
	return ErrBookingInPortal
}

func parsePortalTime(value string) (time.Time, error) {
	when, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return when, nil
	}
	return time.ParseInLocation(wallTime, value, warsaw)
}

// BookAppointment follows the portal's temporary hold and confirmation flow.
func (c *HTTPClient) BookAppointment(ctx context.Context, slot domain.Appointment) (string, error) {
	var term bookingTerm
	if json.Unmarshal(slot.BookingDetails, &term) != nil || term.ScheduleID <= 0 || !slot.DateTime.After(time.Now()) {
		return "", ErrBookingInPortal
	}
	hold, err := c.bookingPost(ctx, lockTermURL, slot.BookingDetails)
	if err != nil {
		return "", err
	}
	var reserved struct {
		ID             json.RawMessage   `json:"temporaryReservationId"`
		AskForReferral bool              `json:"askForReferral"`
		IsStomatology  bool              `json:"isStomatology"`
		IsAlfavox      bool              `json:"isAlfavoxVideoConsultation"`
		RelatedVisits  []json.RawMessage `json:"relatedVisits"`
		Valuations     []json.RawMessage `json:"valuations"`
	}
	if hold.Errors == nil || (*hold.Errors && len(hold.Value) == 0) {
		return "", hold.rejection()
	}
	_ = json.Unmarshal(hold.Value, &reserved)
	id := bookingID(reserved.ID)
	if id == "" {
		return "", hold.rejection()
	}
	release := true
	defer func() {
		if release {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
			defer cancel()
			_, _ = c.request(releaseCtx, http.MethodPost, releaseTermURL+"?reservationId="+url.QueryEscape(id), []byte("{}"))
		}
	}()
	if *hold.Errors || hold.Warnings || reserved.AskForReferral || reserved.IsStomatology || reserved.IsAlfavox ||
		len(reserved.RelatedVisits) != 0 || len(reserved.Valuations) != 1 || !coveredValuation(reserved.Valuations[0]) {
		return "", hold.rejection()
	}

	body, _ := json.Marshal(map[string]any{
		"cityId": term.CityID, "serviceVariantId": term.ServiceID, "doctorId": term.DoctorID,
		"facilityId": term.FacilityID, "roomId": term.RoomID, "scheduleId": term.ScheduleID,
		"date": term.Date, "timeFrom": term.TimeFrom, "temporaryReservationId": reserved.ID,
		"executedTriageId": nil, "referralId": nil, "eReferralId": nil, "parentReservationId": nil,
		"referralRequired": false, "valuationId": nil, "valuation": reserved.Valuations[0],
	})
	release = false
	confirmed, err := c.bookingPost(ctx, confirmTermURL, body)
	if err != nil {
		if errors.Is(err, ErrAuthentication) || errors.Is(err, ErrBrowserBusy) {
			release = true
			return "", err
		}
		return "", ErrBookingUnknown
	}
	if confirmed.Errors != nil && *confirmed.Errors {
		release = true
		return "", confirmed.rejection()
	}
	var result struct {
		ID json.RawMessage `json:"reservationId"`
	}
	if confirmed.Errors == nil || json.Unmarshal(confirmed.Value, &result) != nil || bookingID(result.ID) == "" {
		return "", ErrBookingUnknown
	}
	return bookingID(result.ID), nil
}

func (c *HTTPClient) bookingPost(ctx context.Context, path string, body []byte) (bookingReply, error) {
	var reply bookingReply
	if !c.browser && c.xsrfToken == "" {
		return reply, ErrBookingInPortal
	}
	data, err := c.request(ctx, http.MethodPost, path, body)
	if status, ok := errors.AsType[*HTTPError](err); ok {
		switch {
		case status.login:
			return reply, ErrAuthentication
		case c.browser && status.StatusCode == http.StatusConflict:
			return reply, ErrBrowserBusy
		}
	}
	if err != nil {
		return reply, err
	}
	if json.Unmarshal(data, &reply) != nil {
		return reply, ErrResponse
	}
	return reply, nil
}

func coveredValuation(data json.RawMessage) bool {
	var value struct {
		Type     int      `json:"valuationType"`
		Price    *float64 `json:"price"`
		Referral bool     `json:"isReferralRequired"`
	}
	return json.Unmarshal(data, &value) == nil && (value.Type == 1 || value.Type == 3) &&
		value.Price != nil && *value.Price == 0 && !value.Referral
}

func bookingID(value json.RawMessage) string {
	var id string
	if json.Unmarshal(value, &id) != nil {
		var number json.Number
		if json.Unmarshal(value, &number) != nil {
			return ""
		}
		id = number.String()
	}
	id = strings.TrimSpace(id)
	if id == "0" || len(id) > 128 {
		return ""
	}
	return id
}
