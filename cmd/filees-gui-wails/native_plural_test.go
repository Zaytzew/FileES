package main

import "testing"

// Native notifications count files the way the WebView does (Intl.PluralRules):
// "Zablokowano 3 pliki", never "plik(ów)" (sandbox acceptance, 2026-09-24).
func TestNativePluralFormsFollowTheLanguage(t *testing.T) {
	language, err := loadNativeLanguage()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[int]string{
		"pl": {1: "plik", 2: "pliki", 4: "pliki", 5: "plików", 12: "plików", 14: "plików", 22: "pliki", 25: "plików", 0: "plików"},
		"en": {1: "file", 0: "files", 2: "files"},
		"de": {1: "Datei", 7: "Dateien"},
		"fr": {0: "fichier", 1: "fichier", 2: "fichiers"},
		"es": {1: "archivo", 3: "archivos"},
	}
	for locale, want := range cases {
		language.selectLocale(locale)
		for count, form := range want {
			if got := language.plural("count.files", count); got != form {
				t.Errorf("%s %d: %q, want %q", locale, count, got, form)
			}
		}
	}
	if got := language.plural("count.nothing", 2); got != "[count.nothing]" {
		t.Fatalf("missing key: %q", got)
	}
}
