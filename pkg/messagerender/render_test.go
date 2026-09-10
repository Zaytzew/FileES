package messagerender

import (
	"strings"
	"testing"
	"time"

	"filees/internal/domaincatalog"
	"filees/pkg/errcat"
)

// fromPacks builds a catalogue the way the daemon serves one, so these tests
// exercise the sentences that actually ship rather than fixtures invented
// here. The IPC hop is covered by the contract tests.
func fromPacks(t *testing.T, locale string) *Catalogue {
	t.Helper()
	registry, err := domaincatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	served, ok := registry.Pack(locale)
	if !ok {
		t.Fatalf("no pack for %q", locale)
	}
	fallback, _ := registry.Pack(domaincatalog.BaseLocale)
	convert := func(pack domaincatalog.Pack) map[string]Message {
		out := make(map[string]Message, len(pack.Messages))
		for key, message := range pack.Messages {
			out[key] = Message{Text: message.Text, Plural: message.Plural, Variants: message.Variants}
		}
		return out
	}
	params := map[string][]Param{}
	for key, schema := range domaincatalog.Schemas() {
		for _, field := range schema.Params {
			params[key] = append(params[key], Param{Name: field.Name, Kind: field.Kind})
		}
	}
	return &Catalogue{
		Locale:         locale,
		FallbackLocale: domaincatalog.BaseLocale,
		CatalogID:      registry.Digest(),
		Messages:       convert(served),
		Fallback:       convert(fallback),
		Params:         params,
	}
}

func TestRendersAPlainSentence(t *testing.T) {
	for _, locale := range []string{"pl", "en"} {
		catalogue := fromPacks(t, locale)
		got := catalogue.Render("LOCK-2002", "lock.invalid_path", nil)
		if got == "" || strings.Contains(got, "{") {
			t.Errorf("%s: %q", locale, got)
		}
	}
}

// The ladder is the whole reason the format has three shapes: the reader must
// be told who is holding the file when that is known.
func TestLadderPicksTheMostSpecificRungThatHasItsValues(t *testing.T) {
	catalogue := fromPacks(t, "pl")
	until := time.Now().Add(90 * time.Minute).UTC().Format(time.RFC3339)
	local := func(value string) string {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed.Local().Format("15:04")
	}

	full := catalogue.Render("LOCK-2001", "lock.held_by_other", map[string]string{
		"path": "rysunek.dwg", "holder": "anna", "until": until,
	})
	if !strings.Contains(full, "rysunek.dwg") || !strings.Contains(full, "anna") || !strings.Contains(full, local(until)) {
		t.Fatalf("full = %q", full)
	}

	holderOnly := catalogue.Render("LOCK-2001", "lock.held_by_other", map[string]string{"holder": "anna"})
	if !strings.Contains(holderOnly, "anna") {
		t.Errorf("holder only = %q", holderOnly)
	}
	if strings.Contains(holderOnly, "rysunek.dwg") {
		t.Errorf("holder only invented a path: %q", holderOnly)
	}

	// Nothing arrived: the last rung has to stand on its own, with no holes.
	bare := catalogue.Render("LOCK-2001", "lock.held_by_other", nil)
	if bare == "" || strings.Contains(bare, "{") {
		t.Fatalf("bare = %q", bare)
	}
	if bare == full {
		t.Error("the ladder collapsed to one wording")
	}
}

func TestBothLanguagesUseTheSameLadderRung(t *testing.T) {
	until := time.Date(2026, 9, 11, 11, 41, 0, 0, time.UTC).Format(time.RFC3339)
	details := map[string]string{"path": "rysunek.dwg", "holder": "anna", "until": until}
	polish := fromPacks(t, "pl").Render("LOCK-2001", "lock.held_by_other", details)
	english := fromPacks(t, "en").Render("LOCK-2001", "lock.held_by_other", details)
	if polish == english {
		t.Fatalf("both languages produced %q", polish)
	}
	for _, sentence := range []string{polish, english} {
		// The values are the same in every language; only the wording moves.
		if !strings.Contains(sentence, "rysunek.dwg") || !strings.Contains(sentence, "anna") {
			t.Errorf("sentence lost its arguments: %q", sentence)
		}
	}
}

// A raw diagnostic is unbounded, untranslated and frequently English. It may
// be shown as diagnostics, never built into a sentence.
func TestDiagnosticArgumentsAreNeverSubstituted(t *testing.T) {
	catalogue := fromPacks(t, "pl")
	catalogue.Messages["commit.failed"] = Message{Text: "Nie udało się: {detail}"}
	got := catalogue.Render("COMMIT-3100", "commit.failed", map[string]string{"detail": "svn: E170013"})
	if strings.Contains(got, "E170013") {
		t.Fatalf("diagnostic reached the sentence: %q", got)
	}
}

func TestUndeclaredArgumentsAreNeverSubstituted(t *testing.T) {
	catalogue := fromPacks(t, "pl")
	catalogue.Messages["sync.unknown"] = Message{Text: "Coś: {whatever}"}
	got := catalogue.Render("SYNC-0000", "sync.unknown", map[string]string{"whatever": "x"})
	if strings.Contains(got, "x") {
		t.Fatalf("undeclared argument was substituted: %q", got)
	}
}

// An unknown key stays visible as a gap in the dictionary. This is the alpha
// rule: the catalogue is not a gate that hides unknown faults.
func TestUnknownKeyNamesItsCodeAndKey(t *testing.T) {
	for _, locale := range []string{"pl", "en"} {
		catalogue := fromPacks(t, locale)
		got := catalogue.Render("ZZZ-9999", "not.a.real.key", nil)
		if !strings.Contains(got, "ZZZ-9999") || !strings.Contains(got, "not.a.real.key") {
			t.Errorf("%s: %q", locale, got)
		}
	}
}

func TestUnknownKeyWithoutEvenAFallbackStillSaysSomething(t *testing.T) {
	catalogue := &Catalogue{Messages: map[string]Message{"x": {Text: "x"}}}
	got := catalogue.Render("ZZZ-9999", "not.a.real.key", nil)
	if !strings.Contains(got, "ZZZ-9999") || !strings.Contains(got, "not.a.real.key") {
		t.Fatalf("got %q", got)
	}
}

func TestFallsBackToTheBaseLocale(t *testing.T) {
	catalogue := fromPacks(t, "pl")
	delete(catalogue.Messages, "lock.invalid_path")
	got := catalogue.Render("LOCK-2002", "lock.invalid_path", nil)
	if got == "" || strings.Contains(got, "not.a.real") {
		t.Fatalf("got %q", got)
	}
	if got != catalogue.Fallback["lock.invalid_path"].Text {
		t.Fatalf("got %q, want the base locale's sentence", got)
	}
}

func TestHintsRenderAndHintNoneStaysSilent(t *testing.T) {
	catalogue := fromPacks(t, "pl")
	if got := catalogue.Hint(string(errcat.HintNone)); got != "" {
		t.Errorf("HintNone = %q", got)
	}
	if catalogue.Hint(string(errcat.HintRetry)) != catalogue.Hint(string(errcat.HintRetryLocal)) {
		t.Error("the informal RETRY alias must share the RETRY_LOCAL sentence")
	}
	for _, hint := range []errcat.Hint{errcat.HintRetryLocal, errcat.HintRetryBackoff, errcat.HintRequireAction, errcat.HintAdminOnly} {
		if catalogue.Hint(string(hint)) == "" {
			t.Errorf("hint %s rendered empty", hint)
		}
	}
}

// A caller without a catalogue must keep its previous behaviour rather than
// render blanks into the interface.
func TestNotReadyRendersNothing(t *testing.T) {
	var missing *Catalogue
	if missing.Ready() || missing.Render("X", "y", nil) != "" || missing.Hint("RETRY") != "" {
		t.Fatal("a nil catalogue must render nothing and say so")
	}
	empty := &Catalogue{}
	if empty.Ready() {
		t.Fatal("an empty catalogue is not ready")
	}
}

func TestFormatsValuesByKind(t *testing.T) {
	catalogue := fromPacks(t, "pl")
	catalogue.Params["whale.insufficient_space"] = []Param{{Name: "available_bytes", Kind: errcat.ParamBytes}}
	catalogue.Messages["whale.insufficient_space"] = Message{Text: "Wolne: {available_bytes}"}
	if got := catalogue.Render("WHALE-2005", "whale.insufficient_space", map[string]string{"available_bytes": "5242880"}); !strings.Contains(got, "5.0 MiB") {
		t.Errorf("bytes = %q", got)
	}
	// A value that does not match its declared kind is refused rather than
	// pasted in raw.
	if got := catalogue.Render("WHALE-2005", "whale.insufficient_space", map[string]string{"available_bytes": "lots"}); strings.Contains(got, "lots") {
		t.Errorf("malformed byte count reached the sentence: %q", got)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1024: "1.0 KiB", 5242880: "5.0 MiB"}
	for value, want := range cases {
		if got := FormatBytes(value); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", value, got, want)
		}
	}
}

// The Go side serves "other" for a plural entry because the standard library
// has no CLDR plural rules and hard-coding them per language is exactly the
// branch this design avoids. No shipped pack uses plurals, so the gap cannot
// be hit unnoticed — and this test fails the moment one is added, forcing the
// decision instead of silently showing the wrong grammar.
func TestNoShippedPluralYet(t *testing.T) {
	registry, err := domaincatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range registry.Locales() {
		pack, _ := registry.Pack(language.Code)
		for key, message := range pack.Messages {
			if message.IsPlural() {
				t.Fatalf("%s carries a plural entry for %q: the Go renderer selects its category "+
					"by serving \"other\" and needs a real rule before this ships", language.Code, key)
			}
		}
	}
}
