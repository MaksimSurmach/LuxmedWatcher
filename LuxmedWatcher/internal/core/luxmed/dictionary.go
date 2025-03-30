package luxmed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"LuxmedWatcher/internal/domain"
)

// GetServiceVariantGroups downloads service variant groups from the corresponding endpoint.
// Must be authenticated
func (c *luxmedClient) GetServiceVariantGroups(ctx context.Context) ([]domain.ServiceVariantGroup, error) {
	req, err := c.newAuthRequest(ctx, http.MethodGet, ServiceVariantsGroupsURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch service variant groups: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch service variant groups, status: %d", resp.StatusCode)
	}

	var services struct {
		Groups []domain.ServiceVariantGroup `json:"children"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&services); err != nil {
		return nil, fmt.Errorf("failed to decode service variant groups: %w", err)
	}

	return services.Groups, nil
}

// GetCities downloads cities from the corresponding endpoint
// Must be authenticated
func (c *luxmedClient) GetCities(ctx context.Context) ([]domain.City, error) {
	req, err := c.newAuthRequest(ctx, http.MethodGet, CitiesURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch cities: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch cities, status: %d", resp.StatusCode)
	}

	var cities []domain.City
	if err := json.NewDecoder(resp.Body).Decode(&cities); err != nil {
		return nil, fmt.Errorf("failed to decode cities: %w", err)
	}

	return cities, nil
}

func (c *luxmedClient) GetDoctorsAndFacilities(ctx context.Context, cityID int, serviceVariantID int) (domain.DoctorsAndFacilities, error) {
	req, err := c.newAuthRequest(ctx, http.MethodGet, DoctorsAndFacilitiesURL, nil)
	if err != nil {
		return domain.DoctorsAndFacilities{}, err
	}

	q := req.URL.Query()
	q.Set("cityId", fmt.Sprintf("%d", cityID))
	q.Set("serviceVariantId", fmt.Sprintf("%d", serviceVariantID))
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return domain.DoctorsAndFacilities{}, fmt.Errorf("failed to fetch doctors and facilities: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return domain.DoctorsAndFacilities{}, fmt.Errorf("failed to fetch doctors and facilities, status: %d", resp.StatusCode)
	}

	var doctorsAndFacilities domain.DoctorsAndFacilities
	if err := json.NewDecoder(resp.Body).Decode(&doctorsAndFacilities); err != nil {
		return domain.DoctorsAndFacilities{}, fmt.Errorf("failed to decode doctors and facilities: %w", err)
	}

	return doctorsAndFacilities, nil
}

func (c *luxmedClient) GetPopularServices(ctx context.Context) ([]domain.PopularService, error) {
	req, err := c.newAuthRequest(ctx, http.MethodGet, PopularServicesURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch popular services: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch popular services, status: %d", resp.StatusCode)
	}

	var popularServices struct {
		Services []domain.PopularService `json:"popularServices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&popularServices); err != nil {
		return nil, fmt.Errorf("failed to decode popular services: %w", err)
	}

	return popularServices.Services, nil
}

func (c *luxmedClient) GetRecentData(ctx context.Context) ([]domain.AppointmentRecord, error) {
	req, err := c.newAuthRequest(ctx, http.MethodGet, RecentSearchesURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch recent data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch recent data, status: %d", resp.StatusCode)
	}

	var recentData []domain.AppointmentRecord
	if err := json.NewDecoder(resp.Body).Decode(&recentData); err != nil {
		return nil, fmt.Errorf("failed to decode recent data: %w", err)
	}

	return recentData, nil
}
