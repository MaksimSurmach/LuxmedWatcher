package luxmed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"LuxmedWatcher/internal/domain"

	log "github.com/sirupsen/logrus"
)

func (c *luxmedClient) GetAvailableAppointments(ctx context.Context, params domain.AppointmentSearch) ([]domain.Appointment, error) {
	c.RefreshTokenIfNeeded(ctx)

	u, err := url.Parse("https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/terms/index")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("searchPlace.id", fmt.Sprintf("%d", params.CityID))
	q.Set("searchPlace.type", "0")
	q.Set("serviceVariantId", fmt.Sprintf("%d", params.ServiceVariantID))
	q.Set("languageId", fmt.Sprintf("%d", params.LanguageID))
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

	req, err := c.newAuthRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GetAvailableAppointments request fail: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GetAvailableAppointments status code: %d", resp.StatusCode)
	}

	var raw struct {
		Success         bool `json:"success"`
		TermsForService struct {
			TermsForDays []struct {
				Day   string `json:"day"`
				Terms []struct {
					DateTimeFrom string `json:"dateTimeFrom"`
					DateTimeTo   string `json:"dateTimeTo"`
					Doctor       struct {
						ID        int    `json:"id"`
						FirstName string `json:"firstName"`
						LastName  string `json:"lastName"`
					} `json:"doctor"`
					Clinic   string `json:"clinic"`
					ClinicID int    `json:"clinicId"`
				} `json:"terms"`
			} `json:"termsForDays"`
		} `json:"termsForService"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode terms response: %w", err)
	}
	if !raw.Success {
		return nil, fmt.Errorf("GetAvailableAppointments response failed")
	}

	var results []domain.Appointment
	var time_format = "2006-01-02T15:04:05"
	for _, day := range raw.TermsForService.TermsForDays {
		for _, t := range day.Terms {
			fromT, err := time.Parse(time_format, t.DateTimeFrom)
			if err != nil {
				log.Warn("Failed to parse time: ", t.DateTimeFrom)
				continue
			}
			app := domain.Appointment{
				DateTimeFrom: fromT,
				DoctorID:     t.Doctor.ID,
				DoctorName:   t.Doctor.FirstName + " " + t.Doctor.LastName,
				ClinicID:     t.ClinicID,
				ClinicName:   t.Clinic,
				ServiceName: "Unknown",
				
			}
			results = append(results, app)
		}
	}
	return results, nil
}
