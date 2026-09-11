package main

import (
	"context"
	"strings"
	"testing"
	"time"

	guiapp "filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
)

type stubCatalogueClient struct {
	calls   []string
	results map[string]*contract.MessagesCatalogResult
	err     error
}

func (s *stubCatalogueClient) MessagesCatalog(_ context.Context, locale string) (*contract.MessagesCatalogResult, error) {
	s.calls = append(s.calls, locale)
	if s.err != nil {
		return nil, s.err
	}
	return s.results[locale], nil
}

func catalogueResult(locale, sentence string) *contract.MessagesCatalogResult {
	return &contract.MessagesCatalogResult{
		Schema:         "filees.domain-catalog/v1",
		CatalogID:      "cat-" + locale,
		Locale:         locale,
		FallbackLocale: "en",
		Languages:      []contract.CatalogLanguage{{Code: locale, Name: locale}},
		Messages: map[string]contract.CatalogMessage{
			"passport.replacement_uncertain": {Text: sentence},
		},
		FallbackMessages: map[string]contract.CatalogMessage{
			"passport.replacement_uncertain": {Text: "uncertain"},
		},
	}
}

func waitForCatalogue(t *testing.T, catalogues *domainCatalogues, locale string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if current := catalogues.current.Load(); current != nil && current.Locale == locale {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("catalogue for %q never arrived", locale)
}

// The projection carries keys; the composition turns them into sentences, and
// the path the issue applies to is appended rather than folded into wording.
func TestRenderDomainErrorsUsesTheCatalogueAndKeepsTheInstance(t *testing.T) {
	client := &stubCatalogueClient{results: map[string]*contract.MessagesCatalogResult{
		"pl": catalogueResult("pl", "Wynik zmiany rezerwacji jest niepewny"),
	}}
	catalogues := newDomainCatalogues(client)
	service := &GUIService{}
	service.setDomainCatalogues(catalogues)
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")

	vm := guiapp.ViewModel{Errors: []guiapp.ErrorViewModel{{
		Code:          "LOCK-2103",
		MessageKey:    "passport.replacement_uncertain",
		MessageDetail: "Łódź.dwg",
	}}}
	service.renderDomainErrors(&vm)

	got := vm.Errors[0].Message
	if !strings.Contains(got, "Wynik zmiany rezerwacji") {
		t.Fatalf("message %q did not come from the catalogue", got)
	}
	if !strings.Contains(got, "Łódź.dwg") {
		t.Fatalf("message %q lost the path it applies to", got)
	}
}

// An entry with no key is old data, not a gap to paper over: whatever the
// daemon sent stays exactly as it was.
func TestRenderDomainErrorsLeavesKeylessEntriesAlone(t *testing.T) {
	client := &stubCatalogueClient{results: map[string]*contract.MessagesCatalogResult{
		"pl": catalogueResult("pl", "nieużywane"),
	}}
	catalogues := newDomainCatalogues(client)
	service := &GUIService{}
	service.setDomainCatalogues(catalogues)
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")

	vm := guiapp.ViewModel{Errors: []guiapp.ErrorViewModel{{Code: "NET-4007", Message: "Network unreachable"}}}
	service.renderDomainErrors(&vm)
	if vm.Errors[0].Message != "Network unreachable" {
		t.Fatalf("message = %q; a keyless entry must be left untouched", vm.Errors[0].Message)
	}
}

// A key the catalogue does not carry still names itself, so a missing entry is
// reportable instead of invisible.
func TestRenderDomainErrorsNamesUnknownKeys(t *testing.T) {
	client := &stubCatalogueClient{results: map[string]*contract.MessagesCatalogResult{
		"pl": catalogueResult("pl", "nieużywane"),
	}}
	catalogues := newDomainCatalogues(client)
	service := &GUIService{}
	service.setDomainCatalogues(catalogues)
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")

	vm := guiapp.ViewModel{Errors: []guiapp.ErrorViewModel{{Code: "ZZZ-9999", MessageKey: "not.a.real.key"}}}
	service.renderDomainErrors(&vm)
	for _, want := range []string{"ZZZ-9999", "not.a.real.key"} {
		if !strings.Contains(vm.Errors[0].Message, want) {
			t.Errorf("message %q does not name %q", vm.Errors[0].Message, want)
		}
	}
}

// Switching language asks the daemon again; switching back uses what is
// already held rather than a second round trip.
func TestDomainCataloguesCacheByServedLocale(t *testing.T) {
	client := &stubCatalogueClient{results: map[string]*contract.MessagesCatalogResult{
		"pl": catalogueResult("pl", "polski"),
		"en": catalogueResult("en", "english"),
	}}
	catalogues := newDomainCatalogues(client)
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")
	catalogues.use("en")
	waitForCatalogue(t, catalogues, "en")
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")

	if len(client.calls) != 2 {
		t.Fatalf("calls = %v; the second Polish switch must come from cache", client.calls)
	}
}

// A failed read leaves the previous catalogue in place: the daemon is the
// authority, so the answer is asked for again rather than invented here.
func TestDomainCataloguesKeepThePreviousAnswerOnFailure(t *testing.T) {
	client := &stubCatalogueClient{results: map[string]*contract.MessagesCatalogResult{
		"pl": catalogueResult("pl", "polski"),
	}}
	catalogues := newDomainCatalogues(client)
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")

	client.err = context.DeadlineExceeded
	catalogues.use("en")
	if current := catalogues.current.Load(); current == nil || current.Locale != "pl" {
		t.Fatalf("a failed read replaced the working catalogue: %+v", current)
	}
}

// Without a catalogue the reader still learns which failure it is.
func TestRenderNamesTheIdentityBeforeAnyCatalogueArrives(t *testing.T) {
	catalogues := newDomainCatalogues(&stubCatalogueClient{})
	got := catalogues.render("REPO-2010", "repo.locate_failed", nil)
	for _, want := range []string{"REPO-2010", "repo.locate_failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not name %q", got, want)
		}
	}
	if hint := catalogues.hint("REQUIRE_ACTION"); hint != "" {
		t.Errorf("a missing catalogue invented a hint: %q", hint)
	}
}
