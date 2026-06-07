package i18n

import "testing"

func TestLocalesHaveSameKeys(t *testing.T) {
	catalog, err := Load("ru")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateSameKeys(); err != nil {
		t.Fatal(err)
	}
}
