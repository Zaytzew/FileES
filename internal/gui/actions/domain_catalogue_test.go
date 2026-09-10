package actions

import (
	"testing"

	"filees/internal/domaincatalog"
	"filees/pkg/messagerender"
)

// polishDomainHooks wires a controller to the catalogue the daemon actually
// ships, in the language a Polish reader sees.
//
// The production controller receives these hooks from the Wails composition,
// which reads the catalogue over IPC; the package under test never imports the
// packs itself, and internal/gui/archtest enforces that. Here the packs are
// read directly so the assertions are about the sentences that are really
// shipped rather than about a fixture invented beside the test.
func polishDomainHooks(t *testing.T) (func(code, key string, details map[string]string) string, func(hint string) string) {
	t.Helper()
	registry, err := domaincatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	pack, ok := registry.Pack("pl")
	if !ok {
		t.Fatal("no Polish pack")
	}
	fallback, _ := registry.Pack(domaincatalog.BaseLocale)
	convert := func(source domaincatalog.Pack) map[string]messagerender.Message {
		out := make(map[string]messagerender.Message, len(source.Messages))
		for key, message := range source.Messages {
			out[key] = messagerender.Message{Text: message.Text, Plural: message.Plural, Variants: message.Variants}
		}
		return out
	}
	params := map[string][]messagerender.Param{}
	for key, schema := range domaincatalog.Schemas() {
		for _, field := range schema.Params {
			params[key] = append(params[key], messagerender.Param{Name: field.Name, Kind: field.Kind})
		}
	}
	catalogue := &messagerender.Catalogue{
		Locale:         "pl",
		FallbackLocale: domaincatalog.BaseLocale,
		CatalogID:      registry.Digest(),
		Messages:       convert(pack),
		Fallback:       convert(fallback),
		Params:         params,
	}
	render := func(code, key string, details map[string]string) string {
		if sentence := catalogue.Render(code, key, details); sentence != "" {
			return sentence
		}
		return diagnosticLabel(code, key)
	}
	return render, catalogue.Hint
}

func polishController(t *testing.T, base Config) *Controller {
	t.Helper()
	base.DomainText, base.DomainHint = polishDomainHooks(t)
	return &Controller{cfg: base}
}

// Without a catalogue the controller must still say which failure it is.
// A blank line in place of an error is the one outcome that leaves a reader
// with nothing to report.
func TestControllerWithoutACatalogueNamesTheIdentity(t *testing.T) {
	c := &Controller{}
	got := c.messageLabel("REPO-2010", "repo.locate_failed", nil)
	if got == "" {
		t.Fatal("a controller with no catalogue rendered nothing")
	}
	for _, want := range []string{"REPO-2010", "repo.locate_failed"} {
		if !contains(got, want) {
			t.Errorf("%q does not name %q", got, want)
		}
	}
	if hint := c.hintLabel("REQUIRE_ACTION"); hint != "" {
		t.Errorf("a missing catalogue must not invent a hint: %q", hint)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
