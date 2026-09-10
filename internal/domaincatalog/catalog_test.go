package domaincatalog

import (
	"strings"
	"testing"

	"filees/pkg/errcat"
)

// TestEmbeddedPacksValidate is the build gate. Everything else in this file
// tests the validator; this tests what we are actually shipping.
func TestEmbeddedPacksValidate(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatalf("embedded packs: %v", err)
	}
	locales := registry.Locales()
	if len(locales) < 2 {
		t.Fatalf("expected at least the base locale and one translation, got %v", locales)
	}
	if locales[0].Code != BaseLocale {
		t.Errorf("base locale should come first, got %q", locales[0].Code)
	}
	for _, language := range locales {
		if strings.TrimSpace(language.Name) == "" {
			t.Errorf("%s has no display name", language.Code)
		}
	}
	if registry.Digest() == "" {
		t.Error("registry has no digest")
	}
}

// Gate: every presented key has a complete translation in every shipped
// language. The English fallback covers a reader whose locale nobody has
// written yet — it does not cover a gap in a language we ship.
func TestEveryPresentedKeyIsTranslatedEverywhere(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	keys := SchemaKeys()
	if len(keys) < 100 {
		t.Fatalf("suspiciously few declared keys: %d", len(keys))
	}
	for _, language := range registry.Locales() {
		for _, key := range keys {
			message, ok := registry.Message(language.Code, key)
			if !ok {
				t.Errorf("%s is missing %q", language.Code, key)
				continue
			}
			for _, template := range message.Templates() {
				if strings.TrimSpace(template) == "" {
					t.Errorf("%s has an empty template for %q", language.Code, key)
				}
			}
		}
	}
}

// Gate: the packs speak about the same errors the dictionary knows about,
// with no key invented on the translation side.
func TestPackKeysMatchTheDictionary(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	schemas := Schemas()
	for _, language := range registry.Locales() {
		pack, _ := registry.Pack(language.Code)
		for key := range pack.Messages {
			if _, ok := schemas[key]; !ok {
				t.Errorf("%s carries unknown key %q", language.Code, key)
			}
		}
	}
	for _, spec := range errcat.All() {
		if _, ok := schemas[string(spec.Key)]; !ok {
			t.Errorf("dictionary key %q has no schema entry", spec.Key)
		}
	}
}

// Gate: a hint that carries a sentence has one in the catalogue, and
// HintNone stays silent rather than acquiring a filler sentence.
func TestHintKeysAreCatalogued(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if key := HintKey(errcat.HintNone); key != "" {
		t.Errorf("HintNone should have no sentence, got %q", key)
	}
	if HintKey(errcat.HintRetry) != HintKey(errcat.HintRetryLocal) {
		t.Error("the informal RETRY alias must share the RETRY_LOCAL sentence")
	}
	hints := []errcat.Hint{
		errcat.HintRetry, errcat.HintRetryLocal, errcat.HintRetryBackoff,
		errcat.HintRequireAction, errcat.HintAdminOnly,
	}
	for _, hint := range hints {
		key := HintKey(hint)
		if key == "" {
			t.Errorf("hint %s has no message key", hint)
			continue
		}
		for _, language := range registry.Locales() {
			if _, ok := registry.Message(language.Code, key); !ok {
				t.Errorf("%s is missing hint sentence %q", language.Code, key)
			}
		}
	}
}

// The unknown-message fallback must name the code and the key. A missing
// dictionary entry has to stay visible as a missing dictionary entry.
func TestUnknownMessageNamesCodeAndKey(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range registry.Locales() {
		message, ok := registry.Message(language.Code, KeyUnknownMessage)
		if !ok {
			t.Fatalf("%s has no unknown-message fallback", language.Code)
		}
		names := placeholders(message)
		if !equalStrings(names, []string{"code", "key"}) {
			t.Errorf("%s fallback uses %v, want code and key", language.Code, names)
		}
	}
}

// The reserved-file message is the one key that already had a hand-written
// sentence matrix in Go. It is the reason ladders exist, so its shape is
// asserted rather than left to the generic completeness gate.
func TestReservedFileLadderKeepsItsSpecificity(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var shape []string
	for _, language := range registry.Locales() {
		message, ok := registry.Message(language.Code, "lock.held_by_other")
		if !ok || !message.IsLadder() {
			t.Fatalf("%s does not carry a ladder for lock.held_by_other", language.Code)
		}
		templates := message.Templates()
		// path, holder and until, every combination of them, and a sentence
		// for the case where nothing arrived: the same eight outcomes the Go
		// version produced from its defaults.
		if len(templates) != 8 {
			t.Errorf("%s ladder has %d rungs, want 8", language.Code, len(templates))
		}
		if got := templatePlaceholders(templates[0]); len(got) != 3 {
			t.Errorf("%s starts at %v, want the most specific rung", language.Code, got)
		}
		if got := templatePlaceholders(templates[len(templates)-1]); len(got) != 0 {
			t.Errorf("%s ends needing %v; the last rung must stand alone", language.Code, got)
		}
		current := parameterShape(message)
		if shape == nil {
			shape = current
			continue
		}
		if !equalStrings(shape, current) {
			t.Errorf("%s offers a different ladder shape: %v vs %v", language.Code, current, shape)
		}
	}
}

func base(messages map[string]Message) map[string]Pack {
	return map[string]Pack{
		"en": {Schema: Schema, Locale: "en", Name: "English", DictionaryVersion: "1", Messages: messages},
	}
}

// full returns a pack covering every declared key, so a table case fails for
// the reason it is testing rather than for completeness.
func full(locale, name string, overrides map[string]Message) Pack {
	messages := map[string]Message{}
	for _, key := range SchemaKeys() {
		messages[key] = Message{Text: "text"}
	}
	messages[KeyUnknownMessage] = Message{Text: "unknown {code} {key}"}
	for key, message := range overrides {
		messages[key] = message
	}
	return Pack{Schema: Schema, Locale: locale, Name: name, DictionaryVersion: "1", Messages: messages}
}

func TestValidateRejects(t *testing.T) {
	// A key with a number parameter, for the plural cases.
	numberKey := ""
	diagnosticKey := ""
	for key, schema := range Schemas() {
		for _, field := range schema.Params {
			if field.Kind == errcat.ParamNumber && numberKey == "" {
				numberKey = key
			}
			if field.Kind == errcat.ParamDiagnostic && diagnosticKey == "" {
				diagnosticKey = key
			}
		}
	}
	if numberKey == "" || diagnosticKey == "" {
		t.Fatalf("fixtures need a number key (%q) and a diagnostic key (%q)", numberKey, diagnosticKey)
	}

	cases := []struct {
		name  string
		packs map[string]Pack
		want  string
	}{
		{
			name:  "unknown key",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{"not.a.real.key": {Text: "x"}})},
			want:  "unknown key",
		},
		{
			name: "missing translation",
			packs: func() map[string]Pack {
				pack := full("en", "English", nil)
				delete(pack.Messages, "sync.unknown")
				return map[string]Pack{"en": pack}
			}(),
			want: "missing translation",
		},
		{
			name:  "undeclared parameter",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{"sync.unknown": {Text: "who is {nobody}"}})},
			want:  "undeclared parameter",
		},
		{
			name:  "diagnostic in a sentence",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{diagnosticKey: {Text: "failed: {detail}"}})},
			want:  "diagnostic parameter",
		},
		{
			name: "parameters differ between languages",
			packs: map[string]Pack{
				"en": full("en", "English", map[string]Message{"lock.held_by_other": {Text: "held by {holder}"}}),
				"pl": full("pl", "Polski", map[string]Message{"lock.held_by_other": {Text: "zajęty"}}),
			},
			want: "parameters differ",
		},
		{
			name:  "plural without other",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{numberKey: {Plural: map[string]string{"one": "one"}}})},
			want:  "without an \"other\" variant",
		},
		{
			name:  "unknown plural category",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{numberKey: {Plural: map[string]string{"other": "x", "several": "y"}}})},
			want:  "unknown plural category",
		},
		{
			name:  "plural without a number parameter",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{"sync.unknown": {Plural: map[string]string{"other": "x"}}})},
			want:  "declares no number parameter",
		},
		{
			name:  "empty message",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{"sync.unknown": {Text: "   "}})},
			want:  "is empty",
		},
		{
			name:  "markup",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{"sync.unknown": {Text: "<b>no</b>"}})},
			want:  "contains markup",
		},
		{
			name:  "ladder with one rung",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{"sync.unknown": {Variants: []string{"only"}}})},
			want:  "fewer than two variants",
		},
		{
			name: "ladder that never stands alone",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{
				"lock.held_by_other": {Variants: []string{"held by {holder} until {until}", "held by {holder}"}},
			})},
			want: "ends on a variant that still needs parameters",
		},
		{
			name: "unreachable rung",
			packs: map[string]Pack{"en": full("en", "English", map[string]Message{
				"lock.held_by_other": {Variants: []string{"held by {holder}", "held by {holder} until {until}", "held"}},
			})},
			want: "unreachable behind variant",
		},
		{
			name: "ladder in one language only",
			packs: map[string]Pack{
				"en": full("en", "English", map[string]Message{
					"lock.held_by_other": {Variants: []string{"held by {holder}", "held"}},
				}),
				"pl": full("pl", "Polski", map[string]Message{"lock.held_by_other": {Text: "zajęty"}}),
			},
			want: "parameters differ",
		},
		{
			name: "wrong schema",
			packs: func() map[string]Pack {
				pack := full("en", "English", nil)
				pack.Schema = "filees.domain-catalog/v2"
				return map[string]Pack{"en": pack}
			}(),
			want: "schema",
		},
		{
			name: "missing language name",
			packs: func() map[string]Pack {
				pack := full("en", "", nil)
				return map[string]Pack{"en": pack}
			}(),
			want: "own name",
		},
		{
			name: "missing dictionary version",
			packs: func() map[string]Pack {
				pack := full("en", "English", nil)
				pack.DictionaryVersion = ""
				return map[string]Pack{"en": pack}
			}(),
			want: "dictionary_version",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := Validate(testCase.packs)
			if err == nil {
				t.Fatal("expected a validation failure")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not mention %q", err, testCase.want)
			}
		})
	}
}

func TestValidateAcceptsACompletePack(t *testing.T) {
	packs := map[string]Pack{
		"en": full("en", "English", nil),
		"pl": full("pl", "Polski", nil),
	}
	if err := Validate(packs); err != nil {
		t.Fatalf("complete packs rejected: %v", err)
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	packs := base(map[string]Message{"not.a.key": {Text: "x"}, "also.not.a.key": {Text: "y"}})
	err := Validate(packs)
	if err == nil {
		t.Fatal("expected failures")
	}
	if strings.Count(err.Error(), "unknown key") != 2 {
		t.Fatalf("expected both unknown keys to be reported, got:\n%s", err)
	}
}

func TestDuplicateKeysAreRejected(t *testing.T) {
	raw := []byte(`{"schema":"x","messages":{"a":"1","a":"2"}}`)
	err := checkDuplicateKeys(raw)
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("duplicate key not reported: %v", err)
	}
	if err := checkDuplicateKeys([]byte(`{"messages":{"a":"1","b":"2"}}`)); err != nil {
		t.Fatalf("distinct keys rejected: %v", err)
	}
}

func TestMessageAcceptsOnlyTextOrPlural(t *testing.T) {
	var message Message
	if err := message.UnmarshalJSON([]byte(`"plain"`)); err != nil || message.Text != "plain" {
		t.Fatalf("string: %v %+v", err, message)
	}
	if err := message.UnmarshalJSON([]byte(`{"other":"x"}`)); err != nil || !message.IsPlural() {
		t.Fatalf("plural: %v %+v", err, message)
	}
	if err := message.UnmarshalJSON([]byte(`42`)); err == nil {
		t.Fatal("a number is not a message")
	}
}

func TestDigestFollowsContent(t *testing.T) {
	order := []string{"en"}
	first := digest(order, map[string][]byte{"en": []byte(`{"a":1}`)})
	second := digest(order, map[string][]byte{"en": []byte(`{"a":2}`)})
	if first == second {
		t.Fatal("digest ignored a content change")
	}
	if first != digest(order, map[string][]byte{"en": []byte(`{"a":1}`)}) {
		t.Fatal("digest is not stable")
	}
}
