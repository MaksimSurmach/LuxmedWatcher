package procedures

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"
	"sync"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

//go:embed names.json
var names []byte

var translations = sync.OnceValue(func() map[string]map[string]string {
	var source map[string]map[string]string
	if err := json.Unmarshal(names, &source); err != nil {
		panic(err)
	}
	normalized := make(map[string]map[string]string, len(source))
	for key, values := range source {
		normalized[normalizeName(key)] = values
	}
	return normalized
})

var dashes = strings.NewReplacer("–", "-", "—", "-")

func normalizeName(name string) string {
	return strings.Join(strings.Fields(dashes.Replace(strings.ToLower(name))), " ")
}

// Name returns the display name without changing the portal name used by saved searches.
func Name(locale, source string) string {
	locale = i18n.Normalize(locale, "pl")
	if locale != "pl" {
		if translated := translations()[normalizeName(source)][locale]; translated != "" {
			return translated
		}
	}
	return source
}

func WatchTitle(locale string, watch domain.Watch) string {
	if watch.ServiceName == "" {
		return watch.Name
	}
	label := Name(locale, watch.ServiceName)
	generatedName := watch.ServiceName + " · " + watch.CityName
	if watch.Name != "" && watch.Name != generatedName && watch.Name != watch.ServiceName {
		return watch.Name + "\n" + label
	}
	if watch.CityName != "" {
		label += " · " + watch.CityName
	}
	return label
}

// Search matches the Polish source and the displayed translation before pagination.
func Search(ctx context.Context, db *storage.DB, cityID int, locale, query string, limit, offset int) ([]domain.Procedure, error) {
	locale = i18n.Normalize(locale, "pl")
	query = normalizeName(query)
	if query == "" {
		return db.SearchProceduresPage(ctx, cityID, query, limit, offset)
	}
	items, err := db.SearchProceduresPage(ctx, cityID, "", 0, 0)
	if err != nil {
		return nil, err
	}
	var result []domain.Procedure
	for _, item := range items {
		source := normalizeName(item.Name)
		display := normalizeName(Name(locale, item.Name))
		if !strings.Contains(source, query) && !strings.Contains(display, query) {
			continue
		}
		if offset > 0 {
			offset--
			continue
		}
		result = append(result, item)
		if limit > 0 && len(result) == limit {
			break
		}
	}
	return result, nil
}
