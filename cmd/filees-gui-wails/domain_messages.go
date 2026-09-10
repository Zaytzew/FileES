package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

	mu       sync.Mutex
	byLocale map[string]*messagerender.Catalogue
	pending  map[string]bool
}

func newDomainCatalogues(client domainCatalogueClient) *domainCatalogues {
	return &domainCatalogues{
		client:   client,
		byLocale: map[string]*messagerender.Catalogue{},
		pending:  map[string]bool{},
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
	if cached, ok := d.byLocale[locale]; ok {
		d.mu.Unlock()
		d.current.Store(cached)
		return
	}
	if d.pending[locale] {
		d.mu.Unlock()
		return
	}
	d.pending[locale] = true
	d.mu.Unlock()

	go d.fetch(locale)
}

func (d *domainCatalogues) fetch(locale string) {
	ctx, cancel := context.WithTimeout(context.Background(), domainCatalogueTimeout)
	defer cancel()
	result, err := d.client.MessagesCatalog(ctx, locale)

	d.mu.Lock()
	delete(d.pending, locale)
	if err != nil || result == nil {
		d.mu.Unlock()
		// A failed read leaves the previous catalogue in place. The daemon is
		// the authority, so the answer is asked for again on the next switch
		// or reconnect rather than replaced by something invented here.
		return
	}
	catalogue := catalogueFromResult(result)
	// Key the cache by what was served, not by what was asked for: an
	// unsupported tag is answered in the base locale, and caching that under
	// the requested tag would hide the substitution from a later reader.
	d.byLocale[result.Locale] = catalogue
	d.mu.Unlock()
	d.current.Store(catalogue)
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
	service.domainCatalogue.Store(catalogues)
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
