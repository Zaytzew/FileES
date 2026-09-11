package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	guiapp "filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
	"filees/pkg/messagerender"
)

const domainCatalogueTimeout = 10 * time.Second

type domainCatalogueClient interface {
	MessagesCatalog(ctx context.Context, locale string) (*contract.MessagesCatalogResult, error)
}

// domainCatalogues holds the daemon's message catalogue for the language the
// interface is currently showing.
//
// The daemon owns these sentences and serves them per read, so this is the
// composition's side of that contract: it asks for a locale, keeps the answer
// as an immutable snapshot and hands the controller a way to render. It is
// deliberately not a second catalogue — nothing here authors a sentence, and
// nothing is kept once the daemon has a different one.
type domainCatalogues struct {
	client  domainCatalogueClient
	current atomic.Pointer[messagerender.Catalogue]
	// changed is called after the held catalogue changes, so what is already
	// on screen is rendered again instead of waiting for the next projection.
	changed func()

	mu sync.Mutex
	// wanted is the locale the interface last asked for. An answer for any
	// other locale is stale by the time it arrives and must not be installed.
	wanted string
	// catalogID is the generation the cached entries came from. A daemon of a
	// different build serves a different one, and entries from two
	// generations must never be mixed.
	catalogID string
	// connection counts the daemon connections this provider has seen. Every
	// read carries the number it was dispatched under, so an answer prepared
	// by a connection that is gone can be recognised and dropped: the daemon
	// on the other side of a new one may be a different build entirely.
	connection uint64
	byLocale   map[string]*messagerender.Catalogue
	pending    map[string]bool
}

func newDomainCatalogues(client domainCatalogueClient) *domainCatalogues {
	return &domainCatalogues{
		client:   client,
		byLocale: map[string]*messagerender.Catalogue{},
		pending:  map[string]bool{},
	}
}

// onChanged installs the callback used to re-render what is already displayed.
func (d *domainCatalogues) onChanged(callback func()) {
	if d == nil {
		return
	}
	d.changed = callback
}

// notify re-renders on its own goroutine, never on the caller's.
//
// The tray resolves a language change while holding its own mutex and calls
// use() from inside it; the snapshot observer it installed takes that same
// mutex. Calling back synchronously would deadlock the two against each other.
func (d *domainCatalogues) notify() {
	if d.changed != nil {
		go d.changed()
	}
}

// use switches to a locale, fetching it if this is the first time.
//
// It never blocks: the tray holds its own mutex while resolving a language
// change, and a catalogue read is a round trip to the daemon. Until the answer
// arrives the interface keeps rendering with what it already had, which is the
// contract's rule that fetching a catalogue must not gate the interface.
func (d *domainCatalogues) use(locale string) {
	if d == nil || d.client == nil {
		return
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return
	}
	d.mu.Lock()
	// Record the choice before anything else: a reply for a locale that is no
	// longer wanted is discarded on arrival rather than overruling the user.
	d.wanted = locale
	if cached, ok := d.byLocale[locale]; ok {
		d.mu.Unlock()
		d.current.Store(cached)
		d.notify()
		return
	}
	if d.pending[locale] {
		d.mu.Unlock()
		return
	}
	d.pending[locale] = true
	connection := d.connection
	d.mu.Unlock()

	go d.fetch(locale, connection)
}

// refresh drops what is cached and asks again for the locale in use.
//
// It is called when the daemon connection is re-established, because the
// daemon on the other end may be a different build with a different
// catalogue. Keeping the old answer would show a previous release's wording
// with no sign that anything had changed.
func (d *domainCatalogues) refresh() {
	if d == nil || d.client == nil {
		return
	}
	d.mu.Lock()
	locale := d.wanted
	// A new connection invalidates everything the previous one told us and
	// everything it is still in the middle of telling us. Clearing pending is
	// what lets the read below actually go out: without it an answer still in
	// flight would look like a read already under way, and nothing would ask
	// the new daemon anything.
	d.connection++
	d.byLocale = map[string]*messagerender.Catalogue{}
	d.catalogID = ""
	d.pending = map[string]bool{}
	d.mu.Unlock()
	if locale == "" {
		return
	}
	d.use(locale)
}

func (d *domainCatalogues) fetch(locale string, connection uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), domainCatalogueTimeout)
	defer cancel()
	result, err := d.client.MessagesCatalog(ctx, locale)

	d.mu.Lock()
	if connection != d.connection {
		// Prepared by a connection that is gone. It is not installed and not
		// even cached: it may describe a different daemon build. Nor is the
		// pending flag touched — it belongs to the read that replaced this one.
		d.mu.Unlock()
		return
	}
	delete(d.pending, locale)
	if err != nil || result == nil {
		d.mu.Unlock()
		// A failed read leaves the previous catalogue in place. The daemon is
		// the authority, so the answer is asked for again on the next switch
		// or reconnect rather than replaced by something invented here.
		return
	}
	catalogue := catalogueFromResult(result)
	// A different generation means these entries and the cached ones describe
	// different builds. Drop the old ones rather than let a later switch serve
	// a mixture of two catalogues.
	if d.catalogID != "" && result.CatalogID != d.catalogID {
		d.byLocale = map[string]*messagerender.Catalogue{}
	}
	d.catalogID = result.CatalogID
	// Key the cache by what was served, not by what was asked for: an
	// unsupported tag is answered in the base locale, and caching that under
	// the requested tag would hide the substitution from a later reader.
	d.byLocale[result.Locale] = catalogue
	// The interface may have moved on while this was in flight. Caching the
	// answer is still right; showing it is not.
	stale := d.wanted != locale
	d.mu.Unlock()
	if stale {
		return
	}
	d.current.Store(catalogue)
	d.notify()
}

func catalogueFromResult(result *contract.MessagesCatalogResult) *messagerender.Catalogue {
	convert := func(in map[string]contract.CatalogMessage) map[string]messagerender.Message {
		out := make(map[string]messagerender.Message, len(in))
		for key, message := range in {
			out[key] = messagerender.Message{
				Text:     message.Text,
				Plural:   message.Plural,
				Variants: message.Variants,
			}
		}
		return out
	}
	params := make(map[string][]messagerender.Param, len(result.Params))
	for key, declared := range result.Params {
		converted := make([]messagerender.Param, 0, len(declared))
		for _, param := range declared {
			converted = append(converted, messagerender.Param{Name: param.Name, Kind: errcat.ParamKind(param.Kind)})
		}
		params[key] = converted
	}
	return &messagerender.Catalogue{
		Locale:         result.Locale,
		FallbackLocale: result.FallbackLocale,
		CatalogID:      result.CatalogID,
		Messages:       convert(result.Messages),
		Fallback:       convert(result.FallbackMessages),
		Params:         params,
	}
}

// render returns the sentence for a daemon message.
//
// It never returns an empty string. Before the first catalogue arrives, or
// after a daemon that cannot serve one, the reader gets the code and the key
// instead of a blank line — the same rule that applies to a key missing from
// the catalogue. A gap has to look like a gap.
func (d *domainCatalogues) render(code, key string, details map[string]string) string {
	if d != nil {
		if catalogue := d.current.Load(); catalogue.Ready() {
			if sentence := catalogue.Render(code, key, details); sentence != "" {
				return sentence
			}
		}
	}
	return diagnosticMessage(code, key)
}

// hint returns the sentence for a hint enum, or "" when it carries none.
// Unlike a message, a missing hint is silence rather than a gap: HintNone is
// a real value and a hint is an addition to the sentence, not the sentence.
func (d *domainCatalogues) hint(hint string) string {
	if d == nil {
		return ""
	}
	catalogue := d.current.Load()
	if !catalogue.Ready() {
		return ""
	}
	return catalogue.Hint(hint)
}

func diagnosticMessage(code, key string) string {
	switch {
	case code != "" && key != "":
		return fmt.Sprintf("[%s %s]", code, key)
	case key != "":
		return fmt.Sprintf("[%s]", key)
	case code != "":
		return fmt.Sprintf("[%s]", code)
	default:
		return ""
	}
}

// setDomainCatalogues attaches the catalogue reader once the IPC client
// exists. Presentation keeps working before that: render falls back to naming
// the code and key, which is what an unknown message looks like anyway.
func (service *GUIService) setDomainCatalogues(catalogues *domainCatalogues) {
	// A catalogue that arrives after a screen was drawn has to reach that
	// screen. Without this the wording waits for the next projection change,
	// so the first error after start, and every message right after a language
	// switch, would be shown with the previous catalogue or as a bare code.
	catalogues.onChanged(service.republishRenderedView)
	service.domainCatalogue.Store(catalogues)
}

// republishRenderedView renders the view already held and publishes it again.
//
// It goes through the ordinary projection path, so the window, the tray and
// the journal receive the new wording exactly as they receive any other
// change, with the revision advanced.
func (service *GUIService) republishRenderedView() {
	service.mu.RLock()
	vm := service.view
	ready := service.emitter != nil || service.observer != nil
	service.mu.RUnlock()
	if !ready {
		return
	}
	service.onChange(vm)
}

// useDomainLocale switches the domain catalogue to the language the interface
// resolved. It must not block: the caller holds the tray's mutex.
func (service *GUIService) useDomainLocale(locale string) {
	service.domainCatalogue.Load().use(locale)
}

func (service *GUIService) domainMessage(code, key string, details map[string]string) string {
	return service.domainCatalogue.Load().render(code, key, details)
}

func (service *GUIService) domainHint(hint string) string {
	return service.domainCatalogue.Load().hint(hint)
}

// renderDomainErrors turns the keys the projection carries into sentences.
//
// internal/gui/app deliberately holds no catalogue: it projects state, and the
// wording belongs to the daemon. The composition is where the catalogue lives,
// and every surface reads the view model from here, so rendering once at this
// seam keeps the window, the tray and the journal saying the same thing.
//
// An entry the catalogue cannot name keeps whatever the daemon sent — for
// journal lines written before keys were carried that is the English log
// sentence, which is old data rather than a regression.
func (service *GUIService) renderDomainErrors(vm *guiapp.ViewModel) {
	if vm == nil || len(vm.Errors) == 0 {
		return
	}
	catalogues := service.domainCatalogue.Load()
	if catalogues == nil {
		return
	}
	for i := range vm.Errors {
		entry := &vm.Errors[i]
		if entry.MessageKey == "" {
			continue
		}
		sentence := catalogues.render(entry.Code, entry.MessageKey, nil)
		if sentence == "" {
			continue
		}
		if entry.MessageDetail != "" {
			// The instance is appended, never folded into the wording: the
			// sentence names the class of failure and the path names this one.
			sentence += " — " + entry.MessageDetail
		}
		entry.Message = sentence
	}
}
