package domain

import "time"

type City struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ServiceVariantGroup struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type ReferenceData struct {
	Cities               []City                `json:"cities"`
	ServiceVariantGroups []ServiceVariantGroup `json:"serviceVariantGroups"`
	UpdatedAt            time.Time             `json:"updated_at"`
}

type Facilities struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Doctor struct {
	ID            int          `json:"id"`
	AcademicTitle string       `json:"academicTitle"`
	FirstName     string       `json:"firstName"`
	LastName      string       `json:"lastName"`
	Facilities    []Facilities `json:"facilityGroupIds"`
}

type DoctorsAndFacilities struct {
	Doctors    []Doctor     `json:"doctors"`
	Facilities []Facilities `json:"facilityGroups"`
}

type PopularService struct {
	ID   int    `json:"serviceVariantId"`
	Name string `json:"serviceVariantName"`
}

type Languages struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
