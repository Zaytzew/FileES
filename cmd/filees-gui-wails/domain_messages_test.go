package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

// One daemon build serves one catalogue identity for every language: the
// digest covers all packs at once. Tests that need a second generation pass
// their own id.
const oneBuildCatalogID = "cat-build-1"

func catalogueResult(locale, sentence string) *contract.MessagesCatalogResult {
	return catalogueResultFrom(locale, sentence, oneBuildCatalogID)
}

func catalogueResultFrom(locale, sentence, catalogID string) *contract.MessagesCatalogResult {
	return &contract.MessagesCatalogResult{
		Schema:         "filees.domain-catalog/v1",
		CatalogID:      catalogID,
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

// blockingCatalogueClient lets a test hold one locale's answer back, so a late
// reply lands after the user has already chosen again.
type blockingCatalogueClient struct {
	mu      sync.Mutex
	release map[string]chan struct{}
	results map[string]*contract.MessagesCatalogResult
	calls   []string
}

func (c *blockingCatalogueClient) MessagesCatalog(_ context.Context, locale string) (*contract.MessagesCatalogResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, locale)
	gate := c.release[locale]
	c.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return c.results[locale], nil
}

// A language switch is the user's decision. An answer that arrives after they
// have chosen again must not overrule them: PL → EN → PL used to end up in
// English because the late English reply was installed unconditionally.
func TestLateAnswerDoesNotOverruleTheLatestChoice(t *testing.T) {
	englishGate := make(chan struct{})
	client := &blockingCatalogueClient{
		release: map[string]chan struct{}{"en": englishGate},
		results: map[string]*contract.MessagesCatalogResult{
			"pl": catalogueResult("pl", "polski"),
			"en": catalogueResult("en", "english"),
		},
	}
	catalogues := newDomainCatalogues(client)

	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")

	catalogues.use("en") // still in flight
	catalogues.use("pl") // the user changed their mind; this one is cached

	close(englishGate) // the English answer finally arrives
	time.Sleep(150 * time.Millisecond)

	if current := catalogues.current.Load(); current == nil || current.Locale != "pl" {
		t.Fatalf("a late answer overruled the user: current = %+v", current)
	}
}

// A catalogue arriving after the screen was drawn has to reach that screen,
// otherwise the wording waits for the next unrelated projection change.
func TestArrivingCatalogueTriggersRerender(t *testing.T) {
	gate := make(chan struct{})
	client := &blockingCatalogueClient{
		release: map[string]chan struct{}{"pl": gate},
		results: map[string]*contract.MessagesCatalogResult{"pl": catalogueResult("pl", "polski")},
	}
	catalogues := newDomainCatalogues(client)
	notified := make(chan struct{}, 4)
	catalogues.onChanged(func() { notified <- struct{}{} })

	catalogues.use("pl")
	close(gate)

	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("the arriving catalogue never asked for a re-render")
	}
}

// Switching to a language already held must also re-render: the wording on
// screen is from the previous one.
func TestSwitchingToACachedLocaleAlsoRerenders(t *testing.T) {
	client := &blockingCatalogueClient{results: map[string]*contract.MessagesCatalogResult{
		"pl": catalogueResult("pl", "polski"),
		"en": catalogueResult("en", "english"),
	}}
	catalogues := newDomainCatalogues(client)
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")
	catalogues.use("en")
	waitForCatalogue(t, catalogues, "en")

	notified := make(chan struct{}, 4)
	catalogues.onChanged(func() { notified <- struct{}{} })
	catalogues.use("pl") // served from cache

	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("a cached switch did not ask for a re-render")
	}
}

// A daemon of a different build serves a different catalogue identity. Entries
// cached from the previous one describe a different release and must go, or a
// later switch would serve a mixture of two generations.
func TestNewGenerationInvalidatesTheCache(t *testing.T) {
	client := &blockingCatalogueClient{results: map[string]*contract.MessagesCatalogResult{
		"pl": catalogueResultFrom("pl", "stare", "cat-build-1"),
		"en": catalogueResultFrom("en", "new build", "cat-build-2"),
	}}
	catalogues := newDomainCatalogues(client)
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")

	// The daemon is replaced; the next answer carries a new identity.
	catalogues.use("en")
	waitForCatalogue(t, catalogues, "en")

	client.mu.Lock()
	client.results["pl"] = catalogueResultFrom("pl", "nowe", "cat-build-2")
	client.mu.Unlock()

	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")
	current := catalogues.current.Load()
	if got := current.Messages["passport.replacement_uncertain"].Text; got != "nowe" {
		t.Fatalf("message = %q; the entry from the previous generation survived", got)
	}
}

// Reconnecting may mean a different daemon build, so the held catalogue is
// re-read rather than trusted across the gap.
func TestRefreshReReadsAfterReconnect(t *testing.T) {
	client := &blockingCatalogueClient{results: map[string]*contract.MessagesCatalogResult{
		"pl": catalogueResult("pl", "polski"),
	}}
	catalogues := newDomainCatalogues(client)
	catalogues.use("pl")
	waitForCatalogue(t, catalogues, "pl")

	// current still holds the previous answer, so waiting on it would prove
	// nothing: what has to happen is a second question to the daemon.
	catalogues.refresh()
	for i := 0; i < 200; i++ {
		client.mu.Lock()
		calls := len(client.calls)
		client.mu.Unlock()
		if calls == 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	t.Fatalf("calls = %v; a reconnect must ask the daemon again", client.calls)
}

// Refresh before any language was chosen has nothing to re-read and must not
// invent a request.
func TestRefreshWithoutAChosenLocaleDoesNothing(t *testing.T) {
	client := &blockingCatalogueClient{}
	catalogues := newDomainCatalogues(client)
	catalogues.refresh()
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 0 {
		t.Fatalf("calls = %v", client.calls)
	}
}

// sequencedClient answers each call from its own slot, so a test can hold the
// first read open while a second one is issued.
type sequencedClient struct {
	mu      sync.Mutex
	calls   []string
	gates   []chan struct{}
	results []*contract.MessagesCatalogResult
}

func (c *sequencedClient) MessagesCatalog(_ context.Context, locale string) (*contract.MessagesCatalogResult, error) {
	c.mu.Lock()
	index := len(c.calls)
	c.calls = append(c.calls, locale)
	var gate chan struct{}
	if index < len(c.gates) {
		gate = c.gates[index]
	}
	var result *contract.MessagesCatalogResult
	if index < len(c.results) {
		result = c.results[index]
	}
	c.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if result == nil {
		// Running past the script means the test allowed an interleaving it
		// did not describe. Saying so beats returning nil and failing later
		// somewhere that looks unrelated.
		return nil, fmt.Errorf("unscripted catalogue read %d for %q", index, locale)
	}
	return result, nil
}

func (c *sequencedClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// A reconnect while a read is still in flight must issue a new read, and the
// answer from the connection that is gone must not be taken.
//
// The daemon on the other side of a new connection may be a different build,
// so an answer prepared by the previous one describes a release that is no
// longer running.
func TestReconnectDuringAnInFlightReadStartsOverAndDropsTheOldAnswer(t *testing.T) {
	stale := make(chan struct{})
	client := &sequencedClient{
		gates: []chan struct{}{stale},
		results: []*contract.MessagesCatalogResult{
			catalogueResultFrom("pl", "stare", "cat-build-1"),
			catalogueResultFrom("pl", "nowe", "cat-build-2"),
		},
	}
	catalogues := newDomainCatalogues(client)

	catalogues.use("pl") // in flight, held open
	for i := 0; i < 200 && client.callCount() < 1; i++ {
		time.Sleep(5 * time.Millisecond)
	}

	catalogues.refresh() // the connection came back while that read was open

	for i := 0; i < 200 && client.callCount() < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if got := client.callCount(); got != 2 {
		t.Fatalf("calls = %d; a reconnect must ask again even with a read in flight", got)
	}
	waitForCatalogue(t, catalogues, "pl")
	if got := catalogues.current.Load().Messages["passport.replacement_uncertain"].Text; got != "nowe" {
		t.Fatalf("message = %q, want the answer from the new connection", got)
	}

	close(stale) // the pre-reconnect answer finally arrives
	time.Sleep(150 * time.Millisecond)

	current := catalogues.current.Load()
	if got := current.Messages["passport.replacement_uncertain"].Text; got != "nowe" {
		t.Fatalf("message = %q; an answer from the previous connection was installed", got)
	}
	if current.CatalogID != "cat-build-2" {
		t.Fatalf("catalog id = %q; the held catalogue is not the new generation", current.CatalogID)
	}
}

// An answer from a connection that is gone must not reach the cache either.
// Serving it later from cache would show a previous build's wording with no
// sign that anything had changed.
func TestAnswerFromAPreviousConnectionIsNotCached(t *testing.T) {
	stale := make(chan struct{})
	client := &sequencedClient{
		gates: []chan struct{}{stale},
		results: []*contract.MessagesCatalogResult{
			catalogueResultFrom("en", "old build", "cat-build-1"),
			catalogueResultFrom("en", "new build", "cat-build-2"),
		},
	}
	catalogues := newDomainCatalogues(client)

	catalogues.use("en") // in flight, held open
	for i := 0; i < 200 && client.callCount() < 1; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	catalogues.refresh() // the connection came back while that read was open
	waitForCatalogue(t, catalogues, "en")

	close(stale) // the answer from the connection that is gone finally arrives
	time.Sleep(150 * time.Millisecond)

	catalogues.mu.Lock()
	cached := catalogues.byLocale["en"]
	catalogues.mu.Unlock()
	if cached == nil {
		t.Fatal("the answer from the live connection was not cached")
	}
	if got := cached.Messages["passport.replacement_uncertain"].Text; got != "new build" {
		t.Fatalf("cached message = %q; an answer from the previous connection reached the cache", got)
	}
	if got := catalogues.current.Load().Messages["passport.replacement_uncertain"].Text; got != "new build" {
		t.Fatalf("shown message = %q; an answer from the previous connection was installed", got)
	}
}

// The pending flag of a read issued after the reconnect must survive the
// completion of the one it replaced, or the next switch would ask twice.
func TestStaleCompletionDoesNotClearTheNewPendingRead(t *testing.T) {
	stale := make(chan struct{})
	current := make(chan struct{})
	client := &sequencedClient{
		gates: []chan struct{}{stale, current},
		results: []*contract.MessagesCatalogResult{
			catalogueResultFrom("pl", "stare", "cat-build-1"),
			catalogueResultFrom("pl", "nowe", "cat-build-2"),
		},
	}
	catalogues := newDomainCatalogues(client)

	catalogues.use("pl")
	for i := 0; i < 200 && client.callCount() < 1; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	catalogues.refresh()
	for i := 0; i < 200 && client.callCount() < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}

	close(stale) // the replaced read completes while the new one is open
	time.Sleep(100 * time.Millisecond)

	catalogues.use("pl") // must be recognised as already under way
	time.Sleep(100 * time.Millisecond)
	if got := client.callCount(); got != 2 {
		t.Fatalf("calls = %d; the in-flight read was forgotten", got)
	}
	close(current)
	waitForCatalogue(t, catalogues, "pl")
}
