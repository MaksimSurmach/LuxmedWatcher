package procedures

import (
	"path/filepath"
	"testing"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

func TestProcedureNameUsesSelectedLocaleOrExactPolishSource(t *testing.T) {
	const source = "Konsultacja okulistyczna  — bez badania dna oka"
	for locale, want := range map[string]string{
		"pl":    source,
		"en":    "Ophthalmology consultation without a fundus examination",
		"ru":    "Консультация офтальмолога без осмотра глазного дна",
		"be":    "Кансультацыя афтальмолага без агляду вочнага дна",
		"uk-UA": "Консультація офтальмолога без огляду очного дна",
		"de":    source,
	} {
		if got := Name(locale, source); got != want {
			t.Errorf("%s: got %q, want %q", locale, got, want)
		}
	}
	const different = "Konsultacja okulistyczna z badaniem dna oka"
	if got := Name("uk", different); got != different {
		t.Fatalf("distinct service should preserve Polish label, got %q", got)
	}
	for key, values := range translations() {
		for locale, value := range values {
			if locale == "pl" || i18n.Normalize(locale, "pl") != locale || value == "" {
				t.Errorf("invalid translation of %q for %q", key, locale)
			}
		}
	}
}

func TestSearchMatchesTranslationAndOriginalBeforePaging(t *testing.T) {
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const original = "Konsultacja dermatologiczna"
	city := domain.City{ID: 1, Name: "Warszawa"}
	if err := db.UpsertProcedures(ctx, city, []domain.Procedure{
		{ID: 3, Name: "Badanie bez tłumaczenia"},
		{ID: 12, Name: original},
		{ID: 15, Name: "Konsultacja okulistyczna  - bez badania dna oka"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"ДЭРМАТОЛАГА", "DERMATOLOGICZNA"} {
		results, err := Search(ctx, db, city.ID, "be", query, 1, 0)
		if err != nil || len(results) != 1 || results[0].ID != 12 || results[0].Name != original {
			t.Fatalf("query %q returned %+v, %v", query, results, err)
		}
	}
	results, err := Search(ctx, db, city.ID, "uk", "КОНСУЛЬТАЦІЯ", 1, 1)
	if err != nil || len(results) != 1 || results[0].ID != 15 {
		t.Fatalf("translated search offset returned %+v, %v", results, err)
	}
	results, err = Search(ctx, db, city.ID, "ru", "TŁUMACZENIA", 2, 0)
	if err != nil || len(results) != 1 || results[0].ID != 3 {
		t.Fatalf("untranslated match returned %+v, %v", results, err)
	}
}
