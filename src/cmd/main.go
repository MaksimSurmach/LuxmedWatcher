package main

import (
	"fmt"
	"log"

	"github.com/MaksimSurmach/luxmed_checker/src/config"
	"github.com/MaksimSurmach/luxmed_checker/src/luxmed"
)

func main() {
	// Load configuration
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	// Create AuthClient (automatically logs in)
	authClient, err := luxmed.NewAuthClient(
		"https://portalpacjenta.luxmed.pl/PatientPortal",
		cfg.Credentials.Username,
		cfg.Credentials.Password,
		"cookies.json",
	)
	if err != nil {
		log.Fatalf("Failed to create AuthClient: %v", err)
	}

	// Create Checker
	checker := luxmed.NewChecker(authClient)

	// Define search parameters
	params := map[string]string{
		"searchPlace.id":    "1",
		"searchPlace.name":  cfg.Settings.City,
		"serviceVariantId":  "4480",
		"languageId":        "10",
		"searchDateFrom":    "2024-12-10",
		"searchDateTo":      "2024-12-23",
	}

	// Check for available appointments
	result, err := checker.CheckAppointments(params)
	if err != nil {
		log.Fatalf("Error checking appointments: %v", err)
	}

	// Process the result
	if !result.Success || len(result.TermsForService.TermsForDays) == 0 {
		fmt.Println("No available appointments found.")
		return
	}

	// Display available appointments
	fmt.Println("Available appointments found:")
	for _, day := range result.TermsForService.TermsForDays {
		for _, term := range day.Terms {
			fmt.Printf("Date: %s, Time: %s-%s, Doctor: %s %s, Clinic: %s\n",
				day.Day, term.DateTimeFrom, term.DateTimeTo,
				term.Doctor.FirstName, term.Doctor.LastName, term.Clinic)
		}
	}
}
