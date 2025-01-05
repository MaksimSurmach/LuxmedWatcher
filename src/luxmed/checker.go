package luxmed

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
)

// Appointment contains information about a doctor's appointment
type Appointment struct {
	DoctorName  string `json:"doctor_name"`
	City        string `json:"city"`
	AvailableAt string `json:"available_at"`
}

// Doctor contains information about a doctor
type Doctor struct {
	ID            int    `json:"id"`
	GenderID      int    `json:"genderId"`
	AcademicTitle string `json:"academicTitle"`
	FirstName     string `json:"firstName"`
	LastName      string `json:"lastName"`
}

// Term contains information about a specific appointment time
type Term struct {
	DateTimeFrom   string `json:"dateTimeFrom"`
	DateTimeTo     string `json:"dateTimeTo"`
	Doctor         Doctor `json:"doctor"`
	ClinicID       int    `json:"clinicId"`
	Clinic         string `json:"clinic"`
	ClinicGroup    string `json:"clinicGroup"`
	IsTelemedicine bool   `json:"isTelemedicine"`
}

// Day contains a list of appointments for a specific day
type Day struct {
	Day   string `json:"day"`
	Terms []Term `json:"terms"`
}

// TermsForService contains all appointments for a service
type TermsForService struct {
	ServiceVariantID int   `json:"serviceVariantId"`
	TermsForDays     []Day `json:"termsForDays"`
}

// CheckResponse represents the server's response with available appointments
type CheckResponse struct {
	CorrelationID   string          `json:"correlationId"`
	TermsForService TermsForService `json:"termsForService"`
	Success         bool            `json:"success"`
}

// Checker manages the checking of available appointments
type Checker struct {
	AuthClient *AuthClient
	HTTPClient *http.Client
}

// NewChecker creates a new Checker instance
func NewChecker(authClient *AuthClient) *Checker {
	return &Checker{
		AuthClient: authClient,
		HTTPClient: authClient.HTTPClient,
	}
}

// CheckAppointments sends a request to search for appointments
func (c *Checker) CheckAppointments(params map[string]string) (*CheckResponse, error) {
	// Construct the URL with query parameters
	baseURL := fmt.Sprintf("%s/PatientPortal/NewPortal/terms/index", c.AuthClient.BaseURL)
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("error parsing base URL: %w", err)
	}

	// Add query parameters
	query := u.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	u.RawQuery = query.Encode()

	// Create HTTP request
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	// Add authorization header
	c.AuthClient.AddAuthHeader(req)

	// Send request
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	// Handle Unauthorized response
	if resp.StatusCode == http.StatusUnauthorized {
		log.Println("Token expired, re-authenticating...")
		if err := c.AuthClient.Authenticate(); err != nil {
			return nil, fmt.Errorf("error re-authenticating: %w", err)
		}
		// Retry the request after re-authentication
		return c.CheckAppointments(params)
	}

	// Check for successful response
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}

	// Decode JSON response
	var checkResp CheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&checkResp); err != nil {
		return nil, fmt.Errorf("error decoding JSON response: %w", err)
	}

	return &checkResp, nil
}
