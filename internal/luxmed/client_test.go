package luxmed

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func TestFingerprintStable(t *testing.T) {
	when := time.Date(2026, 6, 12, 14, 30, 0, 0, time.UTC)
	app := domain.Appointment{DateTime: when, ServiceID: 10, DoctorID: 20, FacilityID: 30, DoctorName: "Dr A", FacilityName: "Clinic"}
	first := Fingerprint(app)
	second := Fingerprint(app)
	if first == "" || first != second {
		t.Fatalf("fingerprint is not stable: %q %q", first, second)
	}
}

func TestNormalizeServicesAcceptsGroupedArrayResponse(t *testing.T) {
	payload := decodePayload(t, `[
		{"id": 1, "name": "Konsultacje", "children": [
			{"id": 101, "name": "Konsultacja internisty"},
			{"id": 102, "name": "Konsultacja kardiologa"}
		]},
		{"id": 2, "name": "Badania", "children": [
			{"id": 201, "name": "USG jamy brzusznej"}
		]}
	]`)

	services := normalizeServices(payload)
	assertServices(t, services, []domain.Service{
		{ID: 101, Name: "Konsultacja internisty"},
		{ID: 102, Name: "Konsultacja kardiologa"},
		{ID: 201, Name: "USG jamy brzusznej"},
	})
}

func TestNormalizeServicesAcceptsRootChildrenResponse(t *testing.T) {
	payload := decodePayload(t, `{
		"children": [
			{"id": "301", "name": "Dermatologia"},
			{"id": "302", "name": "Ortopedia"}
		]
	}`)

	services := normalizeServices(payload)
	assertServices(t, services, []domain.Service{
		{ID: 301, Name: "Dermatologia"},
		{ID: 302, Name: "Ortopedia"},
	})
}

func TestNormalizeServicesAcceptsWrappedResponse(t *testing.T) {
	payload := decodePayload(t, `{
		"success": true,
		"data": [
			{"serviceVariantId": 401, "serviceVariantName": "Pediatria"},
			{"serviceId": 402, "serviceName": "Laryngologia"},
			{"serviceVariantId": 401, "serviceVariantName": "Pediatria"}
		]
	}`)

	services := normalizeServices(payload)
	assertServices(t, services, []domain.Service{
		{ID: 401, Name: "Pediatria"},
		{ID: 402, Name: "Laryngologia"},
	})
}

func decodePayload(t *testing.T, raw string) any {
	t.Helper()
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertServices(t *testing.T, got []domain.Service, want []domain.Service) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d services, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("service %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
