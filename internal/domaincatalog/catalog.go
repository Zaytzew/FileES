package domaincatalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

//go:embed catalogs/*.json
var packFS embed.FS

const packDir = "catalogs"

// Message is one catalogue entry in one of three shapes: a plain template,
// the plural variants of one, or a ladder of increasingly specific templates.
//
// The plural categories are selected by the renderer; the daemon does not
// decide what "few" means in a language it was not written for.
//
// The ladder exists because "the file is busy" is not actionable while
// "Anna has it until 13:41" is. Which sentence fits depends on which values
// arrived, so the renderer takes the first variant whose every parameter is
// present and the pack decides the wording — a translator can reorder the
// clause or drop a possessive that does not exist in their language, which
// is exactly what a chain of conditionals in Go could never let them do.
type Message struct {
	Text     string
	Plural   map[string]string
	Variants []string
}

// IsPlural reports whether this entry carries plural variants.
func (m Message) IsPlural() bool { return m.Plural != nil }

// IsLadder reports whether this entry carries specificity variants.
func (m Message) IsLadder() bool { return m.Variants != nil }

// Templates returns every template in the entry, so a caller checking
// placeholders does not have to know which shape it got. For a ladder the
// order is the pack's own: most specific first.
func (m Message) Templates() []string {
	switch {
	case m.IsLadder():
		return append([]string(nil), m.Variants...)
	case m.IsPlural():
		categories := make([]string, 0, len(m.Plural))
		for category := range m.Plural {
			categories = append(categories, category)
		}
		sort.Strings(categories)
		out := make([]string, 0, len(categories))
		for _, category := range categories {
			out = append(out, m.Plural[category])
		}
		return out
	default:
		return []string{m.Text}
	}
}

// UnmarshalJSON accepts a string, an object of plural variants or an array of
// ladder variants, and nothing else. A number here is a malformed pack, not a
// value to coerce.
//
// A ladder of plural objects is deliberately not a shape: two selection rules
// in one entry would make it impossible to say, from the pack alone, which
// sentence a reader will get.
func (m *Message) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		m.Text, m.Plural, m.Variants = text, nil, nil
		return nil
	}
	var variants []string
	if err := json.Unmarshal(raw, &variants); err == nil {
		m.Text, m.Plural, m.Variants = "", nil, variants
		return nil
	}
	var plural map[string]string
	if err := json.Unmarshal(raw, &plural); err != nil {
		return fmt.Errorf("message must be a string, a plural object or an array of variants: %w", err)
	}
	m.Text, m.Plural, m.Variants = "", plural, nil
	return nil
}

// Pack is one language's domain catalogue.
type Pack struct {
	Schema            string             `json:"schema"`
	Locale            string             `json:"locale"`
	Name              string             `json:"name"`
	DictionaryVersion string             `json:"dictionary_version"`
	Messages          map[string]Message `json:"messages"`
}

// Language is one entry of the registry a renderer may offer.
type Language struct {
	Code string
	Name string
}

// Registry is every validated pack in this build.
type Registry struct {
	packs  map[string]Pack
	order  []string
	digest string
}

// Locales returns the available languages, base locale first and the rest in
// a stable order.
func (r Registry) Locales() []Language {
	out := make([]Language, 0, len(r.order))
	for _, code := range r.order {
		out = append(out, Language{Code: code, Name: r.packs[code].Name})
	}
	return out
}

// Pack returns one language's pack.
func (r Registry) Pack(locale string) (Pack, bool) {
	pack, ok := r.packs[locale]
	return pack, ok
}

// Message returns one template without falling back. Fallback is a decision
// for the caller that knows which locale the reader asked for; hiding it here
// would make a missing translation invisible.
func (r Registry) Message(locale, key string) (Message, bool) {
	pack, ok := r.packs[locale]
	if !ok {
		return Message{}, false
	}
	message, ok := pack.Messages[key]
	return message, ok
}

// Digest identifies this catalogue's content. Two renderers holding the same
// digest hold the same templates; a renderer that reconnects compares it
// before reusing what it cached, instead of mixing two generations.
func (r Registry) Digest() string { return r.digest }

// Load reads, validates and returns every embedded pack.
//
// Discovery is by directory, not by a list in the code: a new reviewed pack
// plus a rebuild is the whole procedure, and a file that is present but
// broken fails the build rather than disappearing quietly from the registry.
func Load() (Registry, error) {
	entries, err := fs.ReadDir(packFS, packDir)
	if err != nil {
		return Registry{}, fmt.Errorf("read %s: %w", packDir, err)
	}
	packs := make(map[string]Pack, len(entries))
	sources := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := path.Join(packDir, entry.Name())
		raw, err := packFS.ReadFile(name)
		if err != nil {
			return Registry{}, fmt.Errorf("read %s: %w", name, err)
		}
		var pack Pack
		if err := json.Unmarshal(raw, &pack); err != nil {
			return Registry{}, fmt.Errorf("%s: %w", name, err)
		}
		// The file name is part of the identity: a pack whose declared
		// locale disagrees with its file cannot be reasoned about, and two
		// files claiming one locale must not silently overwrite each other.
		if expected := strings.TrimSuffix(entry.Name(), ".json"); pack.Locale != expected {
			return Registry{}, fmt.Errorf("%s: declares locale %q", name, pack.Locale)
		}
		if _, exists := packs[pack.Locale]; exists {
			return Registry{}, fmt.Errorf("%s: duplicate locale %q", name, pack.Locale)
		}
		if err := checkDuplicateKeys(raw); err != nil {
			return Registry{}, fmt.Errorf("%s: %w", name, err)
		}
		packs[pack.Locale] = pack
		sources[pack.Locale] = raw
	}
	if _, ok := packs[BaseLocale]; !ok {
		return Registry{}, fmt.Errorf("base locale %q is missing", BaseLocale)
	}
	if err := Validate(packs); err != nil {
		return Registry{}, err
	}
	order := make([]string, 0, len(packs))
	for locale := range packs {
		if locale != BaseLocale {
			order = append(order, locale)
		}
	}
	sort.Strings(order)
	order = append([]string{BaseLocale}, order...)
	return Registry{packs: packs, order: order, digest: digest(order, sources)}, nil
}

var shared = sync.OnceValues(Load)

// Shared returns the process-wide registry, parsed once.
func Shared() (Registry, error) { return shared() }
