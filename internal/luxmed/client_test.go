package luxmed

import (
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
