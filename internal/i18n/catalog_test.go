package i18n

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestLocalesHaveSameKeys(t *testing.T) {
	catalog, err := Load("ru")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateSameKeys(); err != nil {
		t.Fatal(err)
	}
}

func TestWebLocalesHaveSameKeysAndPlaceholders(t *testing.T) {
	fields := regexp.MustCompile(`\{[a-zA-Z0-9_]+\}`)
	var baseline map[string]string
	for _, locale := range SupportedLocales {
		data, err := Web(locale)
		if err != nil {
			t.Fatal(err)
		}
		entries := make(map[string]string)
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatalf("%s: %v", locale, err)
		}
		if len(entries) == 0 {
			t.Fatalf("%s: empty UI dictionary", locale)
		}
		if locale == "pl" {
			baseline = entries
		}
		if len(entries) != len(baseline) {
			t.Errorf("%s: %d UI strings, expected %d", locale, len(entries), len(baseline))
		}
		for key, source := range baseline {
			if strings.TrimSpace(entries[key]) == "" {
				t.Errorf("%s: missing UI key %q", locale, key)
				continue
			}
			matches := fields.FindAllString(entries[key], -1)
			slices.Sort(matches)
			want := fields.FindAllString(source, -1)
			slices.Sort(want)
			if !slices.Equal(matches, want) {
				t.Errorf("%s: UI key %q uses %v, expected %v", locale, key, matches, want)
			}
		}
	}
}

func TestWebCalendarLabels(t *testing.T) {
	catalog, err := Load("pl")
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range SupportedLocales {
		t.Run(locale, func(t *testing.T) {
			data, err := Web(locale)
			if err != nil {
				t.Fatal(err)
			}
			entries := make(map[string]string)
			if err := json.Unmarshal(data, &entries); err != nil {
				t.Fatal(err)
			}
			if entries["calendar.weekdays"] != catalog.T(locale, "calendar.weekdays") {
				t.Error("Mini App weekday labels differ from the bot")
			}
			months := strings.Split(entries["calendar.months"], ",")
			if len(months) != 12 || slices.Contains(months, "") {
				t.Error("expected 12 localized month labels")
			}
		})
	}
}

func TestLocalesHaveSamePrintfVerbs(t *testing.T) {
	verbs := regexp.MustCompile(`%[#+\-0-9.]*[a-z]`)
	catalog, err := Load("ru")
	if err != nil {
		t.Fatal(err)
	}
	for key, source := range catalog.messages["en"] {
		want := verbs.FindAllString(source, -1)
		for _, locale := range SupportedLocales {
			value := catalog.messages[locale][key]
			if strings.TrimSpace(value) == "" {
				t.Errorf("%s: empty bot key %q", locale, key)
			}
			got := verbs.FindAllString(value, -1)
			if !slices.Equal(got, want) {
				t.Errorf("%s: key %q uses %v, expected %v", locale, key, got, want)
			}
		}
	}
}
