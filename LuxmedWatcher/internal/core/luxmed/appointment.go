package luxmed

import (
	"time"
)

// Appointment описывает один доступный слот записи к врачу на сайте Luxmed.
type Appointment struct {
	DateTimeFrom time.Time
	DateTimeTo   time.Time

	DoctorID   int
	DoctorName string

	ClinicID   int
	ClinicName string

	// Можно добавить другие поля, если они нужны.
	// Пример: IsTelemedicine, RoomID, PartOfDay, Priority, и т.д.
}

// AppointmentSearch определяет параметры поиска (какого врача ищем, в каком городе и т.д.).
// Эти параметры затем передаются в запрос к Luxmed API.
type AppointmentSearch struct {
	CityID          int    
	CityName        string // опционально, для логов
	ServiceVariantID int    // услуга, которую ищем
	DoctorID        int    // доктор, если хотим искать конкретного врача
	PlaceID     int
	PlaceName   string
	LanguageID        int
	ReferralID        int
	ReferralTypeID    int
	ProcessID         string
	SearchDays		int
}

type rawTermsResponse struct {
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