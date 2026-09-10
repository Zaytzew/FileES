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

// explainLocale is the language Explain answers in.
//
// Android is out of scope for the i18n vertical: it has its own client cycle,
// and this constant is not the beginning of one. Moving the sentences here off
// errcat.Spec.Polish and onto the shipped language pack removes the last
// second source of user text; it does not add a language switch, a new
// envelope field or anything Kotlin can see.
const explainLocale = "pl"

// explainCatalogue is the compiled-in pack, parsed once.
//
// The mobile client has no daemon of its own to ask, so it reads the packs it
// was built with. That is the same data the daemon serves over IPC, from the
// same validated files, rather than a second catalogue kept in step by hand.
var explainCatalogue = sync.OnceValue(func() *messagerender.Catalogue {
	registry, err := domaincatalog.Load()
	if err != nil {
		return nil
	}
	pack, ok := registry.Pack(explainLocale)
	if !ok {
		return nil
	}
	fallback, _ := registry.Pack(domaincatalog.BaseLocale)
	catalogue := &messagerender.Catalogue{
		Locale:         explainLocale,
		FallbackLocale: domaincatalog.BaseLocale,
		CatalogID:      registry.Digest(),
		Messages:       renderMessages(pack),
		Fallback:       renderMessages(fallback),
		Params:         renderParams(),
	}
	return catalogue
})

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
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	entry := errmap.Classify(errors.New(raw))
	if entry.IsNoop() || entry.Key == errcat.KeyUnknown {
		return ""
	}
	catalogue := explainCatalogue()
	if !catalogue.Ready() {
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
