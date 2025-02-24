package luxmed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// GetAvailableAppointments реализует запрос к Luxmed для получения слотов.
func (c *luxmedClient) GetAvailableAppointments(ctx context.Context, params AppointmentSearch) ([]Appointment, error) {
	if err := c.RefreshTokenIfNeeded(ctx); err != nil {
		return nil, fmt.Errorf("refresh token error: %w", err)
	}

	// prepare URL
	u, err := url.Parse("https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/terms/index")
	if err != nil {
		return nil, err
	}
	q := u.Query()

	q.Set("searchPlace.id", fmt.Sprintf("%d", params.CityID))
	q.Set("searchPlace.type", fmt.Sprintf("%d", 0))
	q.Set("serviceVariantId", fmt.Sprintf("%d", params.ServiceVariantID))
	q.Set("languageId", fmt.Sprintf("%d", 10))
	q.Set("searchDateFrom", time.Now().Format("2006-01-02"))
	q.Set("searchDateTo", time.Now().AddDate(0, 0, params.SearchDays).Format("2006-01-02"))
	q.Set("searchDatePreset", "14")
	q.Set("delocalized", "false")
	if params.DoctorID > 0 {
		q.Set("doctorsIds", fmt.Sprintf("%d", params.DoctorID))
	}
	if params.PlaceID > 0 {
		q.Set("facilitiesIds", fmt.Sprintf("%d", params.PlaceID))
	}

	u.RawQuery = q.Encode()

	// Make request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	// Set headers
	if c.tokens.AccessToken != "" {
		req.Header.Set("authorization-token", c.tokens.AccessToken)
	}
	// Set cookies
	for k, v := range c.tokens.Cookies {
		req.AddCookie(&http.Cookie{
			Name:  k,
			Value: v,
		})
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GetAvailableAppointments request fail: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Может Luxmed вернёт 401, если токен истёк => нужно переавторизоваться
		return nil, fmt.Errorf("GetAvailableAppointments status code: %d", resp.StatusCode)
	}

	// Parse response
	var raw rawTermsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode terms response: %w", err)
	}
	if !raw.Success {
		return nil, fmt.Errorf("GetAvailableAppointments response failed")
	}

	// Convert to our struct
	var results []Appointment
	for _, day := range raw.TermsForService.TermsForDays {
		for _, t := range day.Terms {
			fromT, err := time.Parse(time.RFC3339, t.DateTimeFrom)
			if err != nil {
				// Можно пропустить слот или вернуть ошибку
				continue
			}
			toT, err := time.Parse(time.RFC3339, t.DateTimeTo)
			if err != nil {
				continue
			}
			app := Appointment{
				DateTimeFrom: fromT,
				DateTimeTo:   toT,
				DoctorID:     t.Doctor.ID,
				DoctorName:   t.Doctor.FirstName + " " + t.Doctor.LastName,
				ClinicID:     t.ClinicID,
				ClinicName:   t.Clinic,
			}
			results = append(results, app)
		}
	}
	return results, nil
}