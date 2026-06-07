package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed locales/*.json
var localeFS embed.FS

type Catalog struct {
	defaultLocale string
	messages      map[string]map[string]string
}

var SupportedLocales = []string{"en", "ru", "pl"}

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
	if len(locale) > 2 {
		locale = locale[:2]
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
