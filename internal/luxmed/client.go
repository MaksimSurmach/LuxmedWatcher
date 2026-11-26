package luxmed

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

const (
	ServiceVariantsGroupsURL = "/PatientPortal/NewPortal/Dictionary/serviceVariantsGroups"
	CitiesURL                = "/PatientPortal/NewPortal/Dictionary/cities"
	DoctorsAndFacilitiesURL  = "/PatientPortal/NewPortal/Dictionary/facilitiesAndDoctors"
	RecentSearchesURL        = "/PatientPortal/NewPortal/RecentSearchTermsParameters/recentSearchData?includeRecentSearchParameters=true"
	PopularServicesURL       = "/PatientPortal/NewPortal/PopularServicesParameters/popularServicesParameters"
	ReferralServicesURL      = "/PatientPortal/NewPortal/Reservation/GetReferralsForReservation"
)

type Credentials struct {
	Login    string
	Password string
}

type SearchResult struct {
	Appointments []domain.Appointment
	// Checked records a validated date window or verified slots in a partial response.
	// A subsequent ErrAuthentication still invalidates the session.
	Checked bool
	// Authenticated records a successful password authentication during the search.
	Authenticated bool
}

type Client interface {
	Authenticate(ctx context.Context, creds Credentials) error
	SearchAppointments(ctx context.Context, watch domain.Watch) (SearchResult, error)
	GetCities(ctx context.Context) ([]domain.City, error)
	GetServices(ctx context.Context) ([]domain.Service, error)
	GetRecentProcedures(ctx context.Context) ([]domain.Procedure, error)
	GetPopularProcedures(ctx context.Context) ([]domain.Procedure, error)
	GetDoctorsAndFacilities(ctx context.Context, cityID int, serviceID int) (domain.DoctorsAndFacilities, error)
}

func (c *HTTPClient) GetCities(ctx context.Context) ([]domain.City, error) {
	var cities []domain.City
	if err := c.getJSON(ctx, CitiesURL, &cities); err != nil {
		return nil, err
	}
	return cities, nil
}

func (c *HTTPClient) GetServices(ctx context.Context) ([]domain.Service, error) {
	var payload any
	if err := c.getJSON(ctx, ServiceVariantsGroupsURL, &payload); err != nil {
		return nil, err
	}
	services := normalizeServices(payload)
	if len(services) == 0 {
		return nil, fmt.Errorf("luxmed service catalog missing entries: %w", ErrResponse)
	}
	return services, nil
}

func (c *HTTPClient) GetRecentProcedures(ctx context.Context) ([]domain.Procedure, error) {
	var payload struct {
		SearchParameters []struct {
			ServiceVariantID int    `json:"serviceVariantId"`
			SearchName       string `json:"searchName"`
		} `json:"searchParameters"`
	}
	if err := c.getJSON(ctx, RecentSearchesURL, &payload); err != nil {
		return nil, err
	}
	items := make([]domain.Procedure, 0, len(payload.SearchParameters))
	for _, search := range payload.SearchParameters {
		if search.ServiceVariantID > 0 {
			items = append(items, domain.Procedure{ID: search.ServiceVariantID, Name: search.SearchName, IsRecent: true})
		}
	}
	return items, nil
}

func (c *HTTPClient) GetPopularProcedures(ctx context.Context) ([]domain.Procedure, error) {
	var payload any
	if err := c.getJSON(ctx, PopularServicesURL, &payload); err != nil {
		return nil, err
	}

	return popularProceduresFromPayload(payload), nil
}

func (c *HTTPClient) GetReferredProcedures(ctx context.Context) ([]domain.Procedure, error) {
	var payload struct {
		Referrals []struct {
			ServiceVariantID int    `json:"serviceVariantId"`
			ServiceName      string `json:"serviceName"`
		} `json:"referrals"`
	}
	if err := c.getJSON(ctx, ReferralServicesURL, &payload); err != nil {
		return nil, err
	}
	items := make([]domain.Procedure, 0, len(payload.Referrals))
	for _, referral := range payload.Referrals {
		if referral.ServiceVariantID > 0 {
			items = append(items, domain.Procedure{ID: referral.ServiceVariantID, Name: referral.ServiceName})
		}
	}
	return items, nil
}

func popularProceduresFromPayload(payload any) []domain.Procedure {
	seen := make(map[int]bool)
	var procedures []domain.Procedure

	walkJSON(payload, func(node map[string]any) {
		id := intFromAny(firstValue(node,
			"serviceVariantId",
			"serviceId",
		))

		name := stringFromAny(firstValue(node,
			"serviceVariantName",
			"serviceName",
			"searchName",
		))

		if id <= 0 || name == "" || seen[id] {
			return
		}

		seen[id] = true
		procedures = append(procedures, domain.Procedure{ID: id, Name: name})
	})

	return procedures
}

func (c *HTTPClient) GetDoctorsAndFacilities(ctx context.Context, cityID int, serviceID int) (domain.DoctorsAndFacilities, error) {
	query := url.Values{
		"cityId":           {strconv.Itoa(cityID)},
		"serviceVariantId": {strconv.Itoa(serviceID)},
	}

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
	if err := c.getJSON(ctx, DoctorsAndFacilitiesURL+"?"+query.Encode(), &raw); err != nil {
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

func walkJSON(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		for _, child := range typed {
			walkJSON(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkJSON(child, visit)
		}
	}
}

func firstValue(node map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := node[key]; ok {
			return value
		}
	}
	return nil
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}
