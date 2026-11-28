package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed locales/*.json web/*.json
var localeFS embed.FS

// Web returns the UI dictionary for a supported locale.
func Web(locale string) ([]byte, error) {
	return localeFS.ReadFile("web/" + Normalize(locale, "pl") + ".json")
}

type Catalog struct {
	defaultLocale string
	messages      map[string]map[string]string
}

var SupportedLocales = []string{"pl", "en", "ru", "be", "uk"}

func Load(defaultLocale string) (*Catalog, error) {
	c := &Catalog{defaultLocale: Normalize(defaultLocale, "ru"), messages: make(map[string]map[string]string)}
	for _, locale := range SupportedLocales {
		body, err := localeFS.ReadFile("locales/" + locale + ".json")
		if err != nil {
			return nil, err
		}
		var messages map[string]string
		if err := json.Unmarshal(body, &messages); err != nil {
			return nil, fmt.Errorf("load locale %s: %w", locale, err)
		}
		c.messages[locale] = messages
	}
	if err := c.ValidateSameKeys(); err != nil {
		return nil, err
	}
	return c, nil
}

func Normalize(locale string, fallback string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if base, _, found := strings.Cut(locale, "-"); found {
		locale = base
	} else if base, _, found := strings.Cut(locale, "_"); found {
		locale = base
	}
	for _, supported := range SupportedLocales {
		if locale == supported {
			return supported
		}
	}
	if fallback == "" {
		return "ru"
	}
	return fallback
}

func (c *Catalog) T(locale, key string, args ...any) string {
	locale = Normalize(locale, c.defaultLocale)
	value := c.messages[locale][key]
	if value == "" {
		value = c.messages[c.defaultLocale][key]
	}
	if value == "" {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(value, args...)
	}
	return value
}

func (c *Catalog) ValidateSameKeys() error {
	base := c.messages["en"]
	for _, locale := range SupportedLocales {
		for key := range base {
			if _, ok := c.messages[locale][key]; !ok {
				return fmt.Errorf("locale %s missing key %s", locale, key)
			}
		}
		for key := range c.messages[locale] {
			if _, ok := base[key]; !ok {
				return fmt.Errorf("locale %s has extra key %s", locale, key)
			}
		}
	}
	return nil
}

// MatchesInput accepts typed shortcuts even after the user switches languages.
func (c *Catalog) MatchesInput(text, key string) bool {
	for _, locale := range SupportedLocales {
		if value := c.messages[locale][key]; value != "" && strings.EqualFold(value, text) {
			return true
		}
	}
	return false
}
