package i18n

import (
	"encoding/json"
	"regexp"
	"slices"
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
			if entries[key] == "" {
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
