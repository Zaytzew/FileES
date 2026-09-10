package domaincatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"filees/pkg/errcat"
)

// placeholderPattern is the same shape the interface catalogue uses, so a
// translator does not have to hold two substitution rules in their head.
// Values are substituted as text; nothing in a pack is evaluated.
var placeholderPattern = regexp.MustCompile(`\{([a-zA-Z][A-Za-z0-9_]*)\}`)

// localePattern is a conservative BCP-47 subset: enough for pl, en, pt-BR,
// and narrow enough that a file name typo does not become a locale.
var localePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// pluralCategories are the CLDR names. A renderer selects one of these; a
// pack that invents a category would silently never be chosen.
var pluralCategories = map[string]bool{
	"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true,
}

// markupPattern catches a template trying to carry a tag. The real defence is
// that renderers insert catalogue text as text, but a pack has no business
// containing markup at all, and finding out at review time is cheaper than
// finding out from a rendered page.
var markupPattern = regexp.MustCompile(`</?[A-Za-z]`)

// Validate checks every pack against the dictionary and against each other.
//
// It is deliberately strict and reports every problem it finds rather than
// the first: a translator fixing one line at a time learns nothing about the
// other fourteen. The build runs this, so an incomplete pack cannot be
// released behind an English fallback that hides it.
func Validate(packs map[string]Pack) error {
	schemas := Schemas()
	locales := make([]string, 0, len(packs))
	for locale := range packs {
		locales = append(locales, locale)
	}
	sort.Strings(locales)

	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	// Placeholders must be the same in every language: the daemon sends one
	// set of arguments, and a sentence that names an argument its siblings do
	// not is a sentence one reader sees complete and another sees with a hole.
	//
	// For a ladder the whole sequence has to match, not just the union. A
	// language that ships three rungs while another ships one does not have a
	// wording difference; it has one reader who is told who is holding the
	// file and another who is told that somebody is.
	used := map[string]map[string][]string{}

	for _, locale := range locales {
		pack := packs[locale]
		if pack.Schema != Schema {
			report("%s: schema %q, want %q", locale, pack.Schema, Schema)
		}
		if !localePattern.MatchString(pack.Locale) {
			report("%s: locale is not a language tag", locale)
		}
		if strings.TrimSpace(pack.Name) == "" {
			report("%s: missing the language's own name", locale)
		}
		if strings.TrimSpace(pack.DictionaryVersion) == "" {
			report("%s: missing dictionary_version", locale)
		}

		for _, key := range sortedKeys(pack.Messages) {
			schema, known := schemas[key]
			if !known {
				report("%s: unknown key %q", locale, key)
				continue
			}
			message := pack.Messages[key]
			validateMessage(locale, key, schema, message, report)
			if used[key] == nil {
				used[key] = map[string][]string{}
			}
			used[key][locale] = parameterShape(message)
		}

		// Completeness is checked per pack, including the base one: the
		// fallback exists for a reader whose locale nobody has written yet,
		// not for keys somebody forgot in a language we do ship.
		for _, key := range SchemaKeys() {
			if _, ok := pack.Messages[key]; !ok {
				report("%s: missing translation for %q", locale, key)
			}
		}
	}

	for _, key := range sortedKeys(used) {
		var reference []string
		var referenceLocale string
		for _, locale := range locales {
			names, ok := used[key][locale]
			if !ok {
				continue
			}
			if referenceLocale == "" {
				reference, referenceLocale = names, locale
				continue
			}
			if !equalStrings(reference, names) {
				report("%s: parameters differ between %s (%v) and %s (%v)",
					key, referenceLocale, reference, locale, names)
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New("domain catalogue:\n  " + strings.Join(problems, "\n  "))
}

func validateMessage(locale, key string, schema KeySchema, message Message, report func(string, ...any)) {
	if message.IsLadder() {
		validateLadder(locale, key, message.Variants, report)
	} else if message.IsPlural() {
		if len(message.Plural) == 0 {
			report("%s: %q has no plural variants", locale, key)
			return
		}
		if strings.TrimSpace(message.Plural["other"]) == "" {
			report("%s: %q is plural without an \"other\" variant", locale, key)
		}
		for _, category := range sortedKeys(message.Plural) {
			if !pluralCategories[category] {
				report("%s: %q has unknown plural category %q", locale, key, category)
			}
			if strings.TrimSpace(message.Plural[category]) == "" {
				report("%s: %q has an empty %q variant", locale, key, category)
			}
		}
		// A plural entry needs something to count. Without a numeric
		// parameter the renderer has no value to select a category from and
		// would always land on "other" — a variant set that looks translated
		// and never varies.
		if !hasKind(schema, errcat.ParamNumber) {
			report("%s: %q is plural but declares no number parameter", locale, key)
		}
	} else if strings.TrimSpace(message.Text) == "" {
		report("%s: %q is empty", locale, key)
	}

	for _, template := range message.Templates() {
		if markupPattern.MatchString(template) {
			report("%s: %q contains markup", locale, key)
		}
		for _, name := range placeholderPattern.FindAllStringSubmatch(template, -1) {
			field, declared := schema.Param(name[1])
			if !declared {
				report("%s: %q uses undeclared parameter {%s}", locale, key, name[1])
				continue
			}
			if field.Kind == errcat.ParamDiagnostic {
				// Diagnostics are unbounded, untranslated and often
				// English. A renderer may show one, marked as such; a
				// sentence may not be built around it.
				report("%s: %q places diagnostic parameter {%s} in the sentence", locale, key, name[1])
			}
		}
	}
}

// validateLadder checks the rungs of a specificity ladder.
func validateLadder(locale, key string, variants []string, report func(string, ...any)) {
	if len(variants) < 2 {
		// One rung is a plain template wearing an array. Saying so keeps the
		// shape of an entry a reliable signal of how it will be selected.
		report("%s: %q is a ladder with fewer than two variants", locale, key)
		return
	}
	sets := make([][]string, len(variants))
	for i, variant := range variants {
		if strings.TrimSpace(variant) == "" {
			report("%s: %q has an empty variant at position %d", locale, key, i+1)
		}
		sets[i] = templatePlaceholders(variant)
	}
	// The last rung is what a reader gets when nothing arrived, so it has to
	// be a complete sentence on its own. This is the rule Spec.Polish carried
	// in prose — an empty Details map must still produce a whole sentence.
	if len(sets[len(sets)-1]) != 0 {
		report("%s: %q ends on a variant that still needs parameters %v", locale, key, sets[len(sets)-1])
	}
	// A rung that asks for everything an earlier rung asks for can never be
	// reached: whenever its parameters are present, the earlier one already
	// matched. Silently unreachable wording is wasted translation.
	for i := range sets {
		for j := 0; j < i; j++ {
			if containsAll(sets[i], sets[j]) {
				report("%s: %q variant %d is unreachable behind variant %d", locale, key, i+1, j+1)
			}
		}
	}
}

// parameterShape describes what a renderer can select from, in a form two
// languages can be compared by: the kind of entry, and the parameter sets in
// the order selection will consider them.
func parameterShape(message Message) []string {
	switch {
	case message.IsLadder():
		out := make([]string, 0, len(message.Variants)+1)
		out = append(out, "ladder")
		for _, variant := range message.Variants {
			out = append(out, strings.Join(templatePlaceholders(variant), ","))
		}
		return out
	case message.IsPlural():
		// Categories may legitimately differ in whether they name the count
		// — English "one repository" spells no number — so plural entries are
		// compared by the union.
		return []string{"plural", strings.Join(placeholders(message), ",")}
	default:
		return []string{"text", strings.Join(placeholders(message), ",")}
	}
}

// placeholders returns the distinct parameter names a message uses.
func placeholders(message Message) []string {
	seen := map[string]bool{}
	var names []string
	for _, template := range message.Templates() {
		for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				names = append(names, match[1])
			}
		}
	}
	sort.Strings(names)
	return names
}

// templatePlaceholders returns the distinct parameter names of one template.
func templatePlaceholders(template string) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			names = append(names, match[1])
		}
	}
	sort.Strings(names)
	return names
}

// containsAll reports whether every name in want is present in have.
func containsAll(have, want []string) bool {
	for _, name := range want {
		found := false
		for _, candidate := range have {
			if candidate == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func hasKind(schema KeySchema, kind errcat.ParamKind) bool {
	for _, field := range schema.Params {
		if field.Kind == kind {
			return true
		}
	}
	return false
}

// checkDuplicateKeys rejects a JSON object that names one key twice.
//
// encoding/json keeps the last value and says nothing, so two translators
// editing the same pack can both "fix" a key and only one of the sentences
// will ever be shown. The decoder's token stream is the only place this is
// still visible.
func checkDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func(path string) error
	walk = func(path string) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, _ := keyToken.(string)
				if seen[key] {
					return fmt.Errorf("duplicate key %q in %s", key, path)
				}
				seen[key] = true
				if err := walk(path + "." + key); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(path + "[]"); err != nil {
					return err
				}
			}
		}
		// Consume the closing delimiter.
		_, err = decoder.Token()
		return err
	}
	if err := walk("$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing content after the pack object")
	}
	return nil
}

// digest identifies the catalogue by its content, so a renderer can tell one
// generation from another without the daemon inventing a counter that a
// restart would reset.
func digest(order []string, sources map[string][]byte) string {
	sum := sha256.New()
	sum.Write([]byte(Schema))
	for _, locale := range order {
		fmt.Fprintf(sum, "\x00%s\x00%d\x00", locale, len(sources[locale]))
		sum.Write(sources[locale])
	}
	return hex.EncodeToString(sum.Sum(nil))[:32]
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
