package luxmed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

const (
	LoginURL                 = "https://portalpacjenta.luxmed.pl/PatientPortal/Account/LogIn"
	RefreshURL               = "https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Token/Refresh"
	XsrfTokenURL             = "https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/security/getforgerytoken"
	ServiceVariantsGroupsURL = "https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Dictionary/serviceVariantsGroups"
	CitiesURL                = "https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Dictionary/cities"
	DoctorsAndFacilitiesURL  = "https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Dictionary/facilitiesAndDoctors"
	TermsURL                 = "https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/terms/index"
)

type Credentials struct {
	Login    string
	Password string
}

type Client interface {
	Authenticate(ctx context.Context, creds Credentials) error
	SearchAppointments(ctx context.Context, watch domain.Watch) ([]domain.Appointment, error)
	GetCities(ctx context.Context) ([]domain.City, error)
	GetServices(ctx context.Context) ([]domain.Service, error)
	GetDoctorsAndFacilities(ctx context.Context, cityID int, serviceID int) (domain.DoctorsAndFacilities, error)
}

type HTTPClient struct {
	client      *http.Client
	accessToken string
	xsrfToken   string
	cookies     map[string]string
	creds       Credentials
	rawPayloads bool
}

func NewHTTPClient(rawPayloads bool) *HTTPClient {
	return &HTTPClient{
		client:      &http.Client{Timeout: 30 * time.Second},
		cookies:     make(map[string]string),
		rawPayloads: rawPayloads,
	}
}

func (c *HTTPClient) Authenticate(ctx context.Context, creds Credentials) error {
	c.creds = creds
	body, _ := json.Marshal(map[string]string{"login": creds.Login, "password": creds.Password})
	req, err := c.newRequest(ctx, http.MethodPost, LoginURL, body)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for _, cookie := range resp.Cookies() {
		c.cookies[cookie.Name] = cookie.Value
	}

	var result struct {
		Succeeded    bool   `json:"succeded"`
		Token        string `json:"token"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || !result.Succeeded {
		if result.ErrorMessage == "" {
			result.ErrorMessage = resp.Status
		}
		return fmt.Errorf("luxmed authentication failed: %s", result.ErrorMessage)
	}
	c.accessToken = result.Token
	if token, err := c.fetchXSRFToken(ctx); err == nil {
		c.xsrfToken = token
	}
	return nil
}

func (c *HTTPClient) SearchAppointments(ctx context.Context, watch domain.Watch) ([]domain.Appointment, error) {
	if err := c.refreshIfNeeded(ctx); err != nil {
		return nil, err
	}
	u, err := url.Parse(TermsURL)
	if err != nil {
		return nil, err
	}
	from, to := searchRange(watch)
	q := u.Query()
	q.Set("searchPlace.id", strconv.Itoa(watch.CityID))
	q.Set("searchPlace.type", "0")
	q.Set("serviceVariantId", strconv.Itoa(watch.ServiceID))
	q.Set("languageId", "10")
	q.Set("searchDateFrom", from.Format("2006-01-02"))
	q.Set("searchDateTo", to.Format("2006-01-02"))
	q.Set("searchDatePreset", strconv.Itoa(watch.NextDays))
	q.Set("delocalized", "false")
	if watch.DoctorID != nil && *watch.DoctorID > 0 {
		q.Set("doctorsIds", strconv.Itoa(*watch.DoctorID))
	}
	for _, facilityID := range watch.FacilityIDs {
		if facilityID > 0 {
			q.Add("facilitiesIds", strconv.Itoa(facilityID))
		}
	}
	u.RawQuery = q.Encode()

	req, err := c.newRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("luxmed terms status %d: %s", resp.StatusCode, string(body))
	}

	var raw termsResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if !raw.Success {
		return nil, errors.New("luxmed terms response success=false")
	}
	payload := ""
	if c.rawPayloads {
		payload = string(body)
	}
	return normalizeTerms(watch, raw, payload), nil
}

func (c *HTTPClient) GetCities(ctx context.Context) ([]domain.City, error) {
	var cities []domain.City
	if err := c.getJSON(ctx, CitiesURL, &cities); err != nil {
		return nil, err
	}
	return cities, nil
}

func (c *HTTPClient) GetServices(ctx context.Context) ([]domain.Service, error) {
	var raw struct {
		Children []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"children"`
	}
	if err := c.getJSON(ctx, ServiceVariantsGroupsURL, &raw); err != nil {
		return nil, err
	}
	services := make([]domain.Service, 0, len(raw.Children))
	for _, item := range raw.Children {
		services = append(services, domain.Service{ID: item.ID, Name: item.Name})
	}
	return services, nil
}

func (c *HTTPClient) GetDoctorsAndFacilities(ctx context.Context, cityID int, serviceID int) (domain.DoctorsAndFacilities, error) {
	u, _ := url.Parse(DoctorsAndFacilitiesURL)
	q := u.Query()
	q.Set("cityId", strconv.Itoa(cityID))
	q.Set("serviceVariantId", strconv.Itoa(serviceID))
	u.RawQuery = q.Encode()

	var raw struct {
		Doctors []struct {
			ID            int    `json:"id"`
			AcademicTitle string `json:"academicTitle"`
			FirstName     string `json:"firstName"`
			LastName      string `json:"lastName"`
			Facilities    []struct {
				ID      int    `json:"id"`
				Name    string `json:"name"`
				Address string `json:"address"`
			} `json:"facilities"`
		} `json:"doctors"`
		Facilities []struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"facilities"`
	}
	if err := c.getJSON(ctx, u.String(), &raw); err != nil {
		return domain.DoctorsAndFacilities{}, err
	}
	result := domain.DoctorsAndFacilities{}
	for _, doctor := range raw.Doctors {
		d := domain.Doctor{ID: doctor.ID, AcademicTitle: doctor.AcademicTitle, FirstName: doctor.FirstName, LastName: doctor.LastName}
		for _, facility := range doctor.Facilities {
			d.Facilities = append(d.Facilities, domain.Facility{ID: facility.ID, Name: facility.Name, Address: facility.Address})
		}
		result.Doctors = append(result.Doctors, d)
	}
	for _, facility := range raw.Facilities {
		result.Facilities = append(result.Facilities, domain.Facility{ID: facility.ID, Name: facility.Name, Address: facility.Address})
	}
	return result, nil
}

func (c *HTTPClient) getJSON(ctx context.Context, endpoint string, target any) error {
	if err := c.refreshIfNeeded(ctx); err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("luxmed GET %s status %d", endpoint, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (c *HTTPClient) fetchXSRFToken(ctx context.Context) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, XsrfTokenURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}
	return tokenResp.Token, nil
}

func (c *HTTPClient) refreshIfNeeded(ctx context.Context) error {
	if c.accessToken == "" {
		return c.Authenticate(ctx, c.creds)
	}
	req, err := c.newRequest(ctx, http.MethodGet, RefreshURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
	}
	return c.Authenticate(ctx, c.creds)
}

func (c *HTTPClient) newRequest(ctx context.Context, method, endpoint string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.accessToken != "" {
		req.Header.Set("authorization-token", c.accessToken)
	}
	if c.xsrfToken != "" {
		req.Header.Set("X-XSRF-TOKEN", c.xsrfToken)
	}
	for key, value := range c.cookies {
		req.AddCookie(&http.Cookie{Name: key, Value: value})
	}
	return req, nil
}

func searchRange(watch domain.Watch) (time.Time, time.Time) {
	now := time.Now()
	from := startOfDay(now)
	if watch.DateFrom != nil {
		from = startOfDay(*watch.DateFrom)
	}
	days := watch.NextDays
	if days <= 0 {
		days = 14
	}
	to := startOfDay(now.AddDate(0, 0, days))
	if watch.DateTo != nil {
		to = startOfDay(*watch.DateTo)
	}
	return from, to
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

type termsResponse struct {
	Success         bool `json:"success"`
	TermsForService struct {
		TermsForDays []struct {
			Day   string `json:"day"`
			Terms []struct {
				ID           string `json:"id"`
				DateTimeFrom string `json:"dateTimeFrom"`
				DateTimeTo   string `json:"dateTimeTo"`
				Doctor       struct {
					ID        int    `json:"id"`
					FirstName string `json:"firstName"`
					LastName  string `json:"lastName"`
				} `json:"doctor"`
				Clinic   string `json:"clinic"`
				ClinicID int    `json:"clinicId"`
				Address  string `json:"address"`
			} `json:"terms"`
		} `json:"termsForDays"`
	} `json:"termsForService"`
}

func normalizeTerms(watch domain.Watch, raw termsResponse, payload string) []domain.Appointment {
	var apps []domain.Appointment
	for _, day := range raw.TermsForService.TermsForDays {
		for _, term := range day.Terms {
			from, err := time.Parse("2006-01-02T15:04:05", term.DateTimeFrom)
			if err != nil {
				continue
			}
			doctorName := strings.TrimSpace(term.Doctor.FirstName + " " + term.Doctor.LastName)
			app := domain.Appointment{
				ExternalID:   term.ID,
				DateTime:     from,
				ServiceID:    watch.ServiceID,
				ServiceName:  watch.ServiceName,
				DoctorID:     term.Doctor.ID,
				DoctorName:   doctorName,
				FacilityID:   term.ClinicID,
				FacilityName: term.Clinic,
				Address:      term.Address,
				CityID:       watch.CityID,
				CityName:     watch.CityName,
				BookingURL:   "https://portalpacjenta.luxmed.pl/PatientPortal/NewPortal/Reservations/Reservation",
				RawPayload:   payload,
			}
			app.Fingerprint = Fingerprint(app)
			apps = append(apps, app)
		}
	}
	return apps
}

func Fingerprint(app domain.Appointment) string {
	parts := []string{
		app.DateTime.UTC().Format(time.RFC3339),
		strconv.Itoa(app.ServiceID),
		strconv.Itoa(app.DoctorID),
		strconv.Itoa(app.FacilityID),
		strings.ToLower(strings.TrimSpace(app.DoctorName)),
		strings.ToLower(strings.TrimSpace(app.FacilityName)),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}
