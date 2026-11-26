package luxmed

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	randv2 "math/rand/v2"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

const (
	TermsURL       = "/PatientPortal/NewPortal/terms/index"
	oneDayTermsURL = "/PatientPortal/NewPortal/terms/oneDayTerms"
	// shortcut: later dates remain partial when four details are exhausted; persist progress if repeated gaps are a problem.
	maxDayDetailRequests = 4
	dateFormat           = "2006-01-02"
	wallTime             = "2006-01-02T15:04:05"
)

// ErrPartialResults means that any returned slots were verified but some requested days were not.
var ErrPartialResults = errors.New("luxmed search has incomplete coverage")

var warsaw = loadLocation()

func loadLocation() *time.Location {
	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		panic(err)
	}
	return location
}

// Location is the time zone used by the LuxMed Polish patient portal.
func Location() *time.Location { return warsaw }

func SearchWindowOpen(watch domain.Watch, now time.Time) bool {
	from, to := searchRange(watch, now)
	return !from.After(to)
}

// SearchDateBounds returns the first searchable day and the exclusive upper bound.
func SearchDateBounds(now time.Time) (time.Time, time.Time) {
	today := startOfDay(now)
	return today, today.AddDate(0, 0, 91)
}

// CheckTermsAccess validates the protected terms response without searching the saved schedule.
func (c *HTTPClient) CheckTermsAccess(ctx context.Context, watch domain.Watch) error {
	day := startOfDay(time.Now())
	query, err := termsQuery(watch, watch.FacilityIDs, day, day)
	if err != nil {
		return err
	}
	data, err := c.get(ctx, TermsURL+"?"+query.Encode())
	if err != nil {
		return err
	}
	_, checked, err := decodeCheckedTerms(watch, data, false)
	if checked {
		return nil
	}
	return err
}

func (c *HTTPClient) SearchAppointments(ctx context.Context, watch domain.Watch) (result SearchResult, err error) {
	authVersion := c.authVersion
	defer func() { result.Authenticated = c.authVersion != authVersion }()

	if watch.CityID <= 0 || watch.ServiceID <= 0 {
		return SearchResult{}, errors.New("luxmed search requires a city and service")
	}
	if watch.DoctorMode == domain.DoctorModeExact && (watch.DoctorID == nil || *watch.DoctorID <= 0) {
		return SearchResult{}, errors.New("luxmed search requires a selected doctor")
	}
	facilities := make([]int, 0, len(watch.FacilityIDs))
	for _, id := range watch.FacilityIDs {
		if id > 0 {
			facilities = append(facilities, id)
		}
	}
	if watch.FacilityMode == domain.FacilityModeSelected && len(facilities) == 0 {
		return SearchResult{}, errors.New("luxmed search requires a selected facility")
	}

	from, to := searchRange(watch, time.Now())
	seen := make(map[string]bool)
	addMatches := func(apps []domain.Appointment, from, to time.Time) {
		for _, app := range apps {
			day := startOfDay(app.DateTime)
			if day.Before(from) || day.After(to) || seen[app.Fingerprint] {
				continue
			}
			if watch.DoctorID != nil && *watch.DoctorID > 0 && app.DoctorID != *watch.DoctorID {
				continue
			}
			if len(facilities) > 0 && !slices.Contains(facilities, app.FacilityID) {
				continue
			}
			result.Appointments = append(result.Appointments, app)
			seen[app.Fingerprint] = true
		}
	}

	type detailRequest struct {
		day   time.Time
		query url.Values
	}
	var details []detailRequest
	requested := make(map[string]bool)
	var uncovered int
	for !from.After(to) {
		end := from.AddDate(0, 0, 13)
		if end.After(to) {
			end = to
		}
		query, err := termsQuery(watch, facilities, from, end)
		if err != nil {
			return result, searchFailure(result.Checked, err)
		}
		data, err := c.get(ctx, TermsURL+"?"+query.Encode())
		if err != nil {
			return result, searchFailure(result.Checked, err)
		}
		preview, checked, err := decodeCheckedTerms(watch, data, c.rawPayloads)
		result.Checked = result.Checked || checked
		addMatches(preview.apps, from, end)
		if err != nil {
			return result, searchFailure(result.Checked, err)
		}
		slices.SortFunc(preview.days, func(a, b dayInfo) int { return cmp.Compare(a.Day, b.Day) })
		for _, info := range preview.days {
			if info.Counter.Count <= 0 || requested[info.Day] {
				continue
			}
			day, err := time.ParseInLocation(dateFormat, info.Day, warsaw)
			if err != nil {
				uncovered++
				continue
			}
			if day.Before(from) || day.After(end) || !matchesWeekday(day, watch.TimeWindows) ||
				(!info.IsLimitedDay && preview.loadedDays[info.Day] >= info.Counter.Count) {
				continue
			}
			requested[info.Day] = true
			if info.Status != 0 {
				uncovered++
				continue
			}
			detailQuery := maps.Clone(query)
			detailQuery.Set("searchDateFrom", info.Day)
			detailQuery.Set("searchDateTo", info.Day)
			detailQuery.Set("expectedTermsNumber", strconv.Itoa(info.Counter.Count))
			detailQuery.Del("searchDatePreset")
			detailQuery.Del("nextSearch")
			details = append(details, detailRequest{day: day, query: detailQuery})
		}
		from = end.AddDate(0, 0, 1)
	}

	if len(details) > maxDayDetailRequests {
		uncovered += len(details) - maxDayDetailRequests
		if watch.LastCheckedAt != nil {
			// Preserve the earliest day and give later days a chance on subsequent checks.
			rest := details[1:]
			randv2.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
		}
		details = details[:maxDayDetailRequests]
	}
	for _, detail := range details {
		data, err := c.get(ctx, oneDayTermsURL+"?"+detail.query.Encode())
		if err != nil {
			return result, searchFailure(true, err)
		}
		apps, err := decodeOneDayTerms(watch, detail.day, data, c.rawPayloads)
		addMatches(apps, detail.day, detail.day)
		if err != nil {
			return result, searchFailure(true, err)
		}
	}
	if uncovered > 0 {
		return result, fmt.Errorf("%w: %d candidate days could not be verified", ErrPartialResults, uncovered)
	}
	return result, nil
}

func matchesWeekday(day time.Time, windows []domain.TimeWindow) bool {
	if len(windows) == 0 {
		return true
	}
	weekday := int(day.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return slices.ContainsFunc(windows, func(window domain.TimeWindow) bool {
		return window.Weekday == 0 || window.Weekday == weekday
	})
}

func searchFailure(scanned bool, err error) error {
	if scanned {
		return fmt.Errorf("%w: %w", ErrPartialResults, err)
	}
	return err
}

func termsQuery(watch domain.Watch, facilities []int, from, to time.Time) (url.Values, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("generate luxmed search correlation ID: %w", err)
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	query := url.Values{
		"searchPlace.id":            {strconv.Itoa(watch.CityID)},
		"searchPlace.type":          {"0"},
		"serviceVariantId":          {strconv.Itoa(watch.ServiceID)},
		"languageId":                {"10"},
		"searchDateFrom":            {from.Format(dateFormat)},
		"searchDateTo":              {to.Format(dateFormat)},
		"searchDatePreset":          {"14"},
		"processId":                 {fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])},
		"nextSearch":                {"false"},
		"searchByMedicalSpecialist": {"false"},
		"locationReplaced":          {"false"},
		"delocalized":               {"false"},
	}
	if watch.CityName != "" {
		query.Set("searchPlace.name", watch.CityName)
	}
	if watch.DoctorID != nil && *watch.DoctorID > 0 {
		query.Set("doctorsIds", strconv.Itoa(*watch.DoctorID))
	}
	for _, id := range facilities {
		query.Add("facilitiesIds", strconv.Itoa(id))
	}
	return query, nil
}

func searchRange(watch domain.Watch, now time.Time) (time.Time, time.Time) {
	today, end := SearchDateBounds(now)
	horizon := end.AddDate(0, 0, -1)
	from := today
	if watch.DateFrom != nil {
		if date := startOfDay(*watch.DateFrom); date.After(from) {
			from = date
		}
	}
	days := watch.NextDays
	if days <= 0 {
		days = 14
	}
	to := from.AddDate(0, 0, min(days, 90)-1)
	if watch.DateTo != nil {
		to = startOfDay(*watch.DateTo)
	}
	if to.After(horizon) {
		to = horizon
	}
	return from, to
}

func decodeCheckedTerms(watch domain.Watch, body []byte, raw bool) (termsPreview, bool, error) {
	preview, err := decodeTermsResponse(watch, body, raw)
	return preview, err == nil || len(preview.apps) > 0, err
}

func startOfDay(t time.Time) time.Time {
	year, month, day := t.In(warsaw).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, warsaw)
}

type termsResponse struct {
	Success         *bool           `json:"success"`
	TermsForService json.RawMessage `json:"termsForService"`
}

type termsDay struct {
	Day   string `json:"day"`
	Terms []struct {
		ID           json.RawMessage `json:"id"`
		DateTimeFrom string          `json:"dateTimeFrom"`
		Doctor       struct {
			ID        int    `json:"id"`
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"doctor"`
		Clinic      string `json:"clinic"`
		ClinicID    int    `json:"clinicId"`
		Address     string `json:"address"`
		ClinicGroup string `json:"clinicGroup"`
	} `json:"terms"`
}

type dayInfo struct {
	Day          string `json:"day"`
	Status       int    `json:"termsStatus"`
	IsLimitedDay bool   `json:"isLimitedDay"`
	Counter      struct {
		Count int `json:"termsNumber"`
	} `json:"termsCounter"`
}

type termsPreview struct {
	apps       []domain.Appointment
	days       []dayInfo
	loadedDays map[string]int
}

func decodeTermsResponse(watch domain.Watch, body []byte, raw bool) (termsPreview, error) {
	var response termsResponse
	if json.Unmarshal(body, &response) != nil {
		return termsPreview{}, fmt.Errorf("luxmed terms: %w", ErrResponse)
	}
	if response.Success != nil && !*response.Success {
		return termsPreview{}, fmt.Errorf("luxmed terms rejected: %w", ErrResponse)
	}
	if bytes.Equal(response.TermsForService, []byte("null")) && response.Success != nil && *response.Success {
		return termsPreview{}, nil
	}
	var service struct {
		Days json.RawMessage `json:"termsForDays"`
		Info []dayInfo       `json:"termsInfoForDays"`
	}
	var days []termsDay
	if json.Unmarshal(response.TermsForService, &service) != nil || json.Unmarshal(service.Days, &days) != nil {
		return termsPreview{}, fmt.Errorf("luxmed terms missing days: %w", ErrResponse)
	}
	apps, err := normalizeTerms(watch, days, payload(body, raw))
	loaded := make(map[string]int)
	for _, app := range apps {
		loaded[app.DateTime.Format(dateFormat)]++
	}
	return termsPreview{apps: apps, days: service.Info, loadedDays: loaded}, err
}

func decodeOneDayTerms(watch domain.Watch, day time.Time, body []byte, raw bool) ([]domain.Appointment, error) {
	var response struct {
		ServiceID int       `json:"serviceVariantId"`
		Day       *termsDay `json:"termsForDay"`
		Info      *dayInfo  `json:"termsInfoForDay"`
	}
	if json.Unmarshal(body, &response) != nil || response.Day == nil || response.Day.Terms == nil ||
		response.ServiceID != watch.ServiceID || response.Day.Day != day.Format(dateFormat) {
		return nil, fmt.Errorf("luxmed one-day terms: %w", ErrResponse)
	}
	apps, err := normalizeTerms(watch, []termsDay{*response.Day}, payload(body, raw))
	for _, app := range apps {
		if !startOfDay(app.DateTime).Equal(day) {
			return nil, fmt.Errorf("luxmed one-day terms outside requested date: %w", ErrResponse)
		}
	}
	if response.Info != nil && (response.Info.IsLimitedDay || response.Info.Counter.Count > len(apps)) {
		err = errors.Join(err, ErrPartialResults)
	}
	return apps, err
}

func payload(body []byte, raw bool) string {
	if raw {
		return string(body)
	}
	return ""
}

func normalizeTerms(watch domain.Watch, days []termsDay, payload string) ([]domain.Appointment, error) {
	var apps []domain.Appointment
	var malformed int
	for _, day := range days {
		for _, term := range day.Terms {
			when, err := time.Parse(time.RFC3339Nano, term.DateTimeFrom)
			if err != nil {
				when, err = time.ParseInLocation(wallTime, term.DateTimeFrom, warsaw)
			}
			if err != nil {
				malformed++
				continue
			}
			var externalID string
			if json.Unmarshal(term.ID, &externalID) != nil {
				var numericID json.Number
				if json.Unmarshal(term.ID, &numericID) == nil {
					externalID = numericID.String()
				}
			}
			address := strings.TrimSpace(term.Address)
			if address == "" {
				address = strings.TrimSpace(term.ClinicGroup)
			}
			app := domain.Appointment{
				ExternalID:   externalID,
				DateTime:     when.In(warsaw),
				ServiceID:    watch.ServiceID,
				ServiceName:  watch.ServiceName,
				DoctorID:     term.Doctor.ID,
				DoctorName:   strings.TrimSpace(term.Doctor.FirstName + " " + term.Doctor.LastName),
				FacilityID:   term.ClinicID,
				FacilityName: term.Clinic,
				Address:      address,
				CityID:       watch.CityID,
				CityName:     watch.CityName,
				BookingURL:   ReservationPageURL,
				RawPayload:   payload,
			}
			app.Fingerprint, app.LegacyFingerprint = appointmentFingerprints(app)
			if app.ExternalID == "" {
				app.ExternalID = app.Fingerprint
			}
			if app.FacilityName == "" {
				app.FacilityName = address
			}
			apps = append(apps, app)
		}
	}
	if malformed > 0 {
		return apps, fmt.Errorf("luxmed terms: %d appointment times could not be parsed: %w", malformed, ErrResponse)
	}
	return apps, nil
}

func Fingerprint(app domain.Appointment) string {
	fingerprint, _ := appointmentFingerprints(app)
	return fingerprint
}

func appointmentFingerprints(app domain.Appointment) (fingerprint, legacy string) {
	// Keep existing slot IDs from when wall-clock dates were parsed as UTC.
	parts := []string{
		app.DateTime.Format(wallTime) + "Z",
		strconv.Itoa(app.ServiceID),
		strconv.Itoa(app.DoctorID),
		strconv.Itoa(app.FacilityID),
		strings.ToLower(strings.TrimSpace(app.DoctorName)),
		strings.ToLower(strings.TrimSpace(app.FacilityName)),
	}
	if address := domain.NormalizeAppointmentAddress(app.Address); app.FacilityID <= 0 && address != "" {
		return hashFingerprint(append(parts, address)), hashFingerprint(parts)
	}
	return hashFingerprint(parts), ""
}

func hashFingerprint(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}
