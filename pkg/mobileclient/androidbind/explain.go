package androidbind

import (
	"errors"
	"strings"
	"sync"

	"filees/internal/domaincatalog"
	"filees/pkg/errcat"
	"filees/pkg/errmap"
	"filees/pkg/messagerender"
)

// explainLocale is the language Explain answers in when the UI does not pass one.
const explainLocale = "pl"

var (
	explainRegistry = sync.OnceValue(func() domaincatalog.Registry {
		registry, err := domaincatalog.Load()
		if err != nil {
			return domaincatalog.Registry{}
		}
		return registry
	})
	explainCatalogues sync.Map // locale -> *messagerender.Catalogue
)

func normalizeExplainLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(locale, "_-"); i > 0 {
		locale = locale[:i]
	}
	switch locale {
	case "pl", "en", "de", "fr", "es":
		return locale
	default:
		return explainLocale
	}
}

func catalogueFor(locale string) *messagerender.Catalogue {
	locale = normalizeExplainLocale(locale)
	if cached, ok := explainCatalogues.Load(locale); ok {
		return cached.(*messagerender.Catalogue)
	}
	registry := explainRegistry()
	pack, ok := registry.Pack(locale)
	if !ok {
		pack, ok = registry.Pack(explainLocale)
		if !ok {
			return nil
		}
		locale = explainLocale
	}
	fallback, _ := registry.Pack(domaincatalog.BaseLocale)
	catalogue := &messagerender.Catalogue{
		Locale:         locale,
		FallbackLocale: domaincatalog.BaseLocale,
		CatalogID:      registry.Digest(),
		Messages:       renderMessages(pack),
		Fallback:       renderMessages(fallback),
		Params:         renderParams(),
	}
	actual, _ := explainCatalogues.LoadOrStore(locale, catalogue)
	return actual.(*messagerender.Catalogue)
}

func renderMessages(pack domaincatalog.Pack) map[string]messagerender.Message {
	out := make(map[string]messagerender.Message, len(pack.Messages))
	for key, message := range pack.Messages {
		out[key] = messagerender.Message{Text: message.Text, Plural: message.Plural, Variants: message.Variants}
	}
	return out
}

func renderParams() map[string][]messagerender.Param {
	schemas := domaincatalog.Schemas()
	out := make(map[string][]messagerender.Param, len(schemas))
	for key, schema := range schemas {
		if len(schema.Params) == 0 {
			continue
		}
		params := make([]messagerender.Param, 0, len(schema.Params))
		for _, field := range schema.Params {
			params = append(params, messagerender.Param{Name: field.Name, Kind: field.Kind})
		}
		out[key] = params
	}
	return out
}

// Explain maps a transport/worker error to the catalogue sentence for it.
// Unknown text returns "" so the UI keeps its local fallback.
//
// Explain is a presentation surface of its own and is deliberately not the
// same thing as the message that travels in filees.mobile/v1: that envelope
// carries the English diagnostic, as it always has. Nothing here changes what
// goes over the wire.
func Explain(raw string) string {
	return ExplainIn(raw, explainLocale)
}

// ExplainIn is Explain in the phone UI language (pl, en, de, fr, es).
// Unknown locales keep Polish, the FileES default.
func ExplainIn(raw, locale string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	entry := errmap.Classify(errors.New(raw))
	if entry.IsNoop() || entry.Key == errcat.KeyUnknown {
		return ""
	}
	catalogue := catalogueFor(locale)
	if catalogue == nil || !catalogue.Ready() {
		// The packs are compiled in and validated by the build, so this is a
		// broken binary rather than a missing translation. There is no second
		// catalogue to fall back to on purpose: the UI keeps its own local
		// text, exactly as it does for an unclassified error.
		return ""
	}
	// Classify carries no structured details, so a message with a ladder
	// resolves to its standalone rung — which is the sentence this function
	// has always returned.
	return catalogue.Render(string(entry.Code), string(entry.Key), nil)
}
