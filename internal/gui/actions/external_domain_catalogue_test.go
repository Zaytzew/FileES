package actions_test

import (
	"sync"

	"filees/internal/domaincatalog"
	"filees/internal/gui/actions"
	"filees/pkg/messagerender"
)

// polishDomainHooks gives a test controller the catalogue the daemon ships.
//
// In production the Wails composition reads this over IPC and hands the
// controller the two hooks; the controller itself never holds a catalogue,
// which is why these tests have to supply them. Reading the real packs rather
// than a fixture keeps assertions about what a user reads honest.
var polishDomainHooks = sync.OnceValues(func() (func(code, key string, details map[string]string) string, func(hint string) string) {
	registry, err := domaincatalog.Load()
	if err != nil {
		// The packs are compiled in and validated by the build, so this can
		// only mean the binary under test is broken.
		panic(err)
	}
	pack, ok := registry.Pack("pl")
	if !ok {
		panic("no Polish pack")
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
		if code != "" {
			return "[" + code + " " + key + "]"
		}
		return "[" + key + "]"
	}
	return render, catalogue.Hint
})

// withDomainCatalogue fills in the hooks a live composition would supply,
// leaving anything a test set explicitly alone.
func withDomainCatalogue(cfg actions.Config) actions.Config {
	if cfg.DomainText == nil || cfg.DomainHint == nil {
		render, hint := polishDomainHooks()
		if cfg.DomainText == nil {
			cfg.DomainText = render
		}
		if cfg.DomainHint == nil {
			cfg.DomainHint = hint
		}
	}
	return cfg
}
