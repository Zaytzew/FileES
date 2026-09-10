// Package messagerender turns a domain message key and its structured
// arguments into a sentence for a reader.
//
// The daemon owns the catalogue and sends keys, arguments and their kinds.
// This package is the other half of that contract: it picks the template,
// substitutes the values and formats the ones that belong to the reader's
// locale rather than to the daemon. It holds no catalogue of its own, knows
// no error codes, and never decides what an operation does.
//
// It deliberately takes neutral types rather than the IPC envelope, so the
// GUI can render without importing the wire package — the same boundary the
// presentation seam in internal/gui/actions already keeps.
package messagerender

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filees/pkg/errcat"
)

// placeholderPattern matches the catalogue's named parameters. It is the same
// shape the interface catalogue and the pack validator use.
var placeholderPattern = regexp.MustCompile(`\{([a-zA-Z][A-Za-z0-9_]*)\}`)

// UnknownMessageKey is the catalogue's own entry for a key it does not carry.
// It names the code and the key, because a missing dictionary entry has to
// stay visible as one rather than become "an error occurred".
const UnknownMessageKey = "catalog.unknown_message"

// Message is one catalogue entry in one of three shapes: a plain template,
// plural variants, or a ladder of increasingly specific templates.
type Message struct {
	Text     string
	Plural   map[string]string
	Variants []string
}

// Param is one declared argument of a message and what the value means.
type Param struct {
	Name string
	Kind errcat.ParamKind
}

// Catalogue is one atomic snapshot: the served locale's templates, the base
// locale's templates from the same read, and the locale-independent schema.
type Catalogue struct {
	Locale         string
	FallbackLocale string
	// CatalogID identifies the content a snapshot came from. A caller that
	// reconnects compares it before reusing a cached catalogue.
	CatalogID string
	Messages  map[string]Message
	Fallback  map[string]Message
	Params    map[string][]Param
}

// Ready reports whether this catalogue can render anything at all. A caller
// holding a nil or empty catalogue must keep its previous behaviour rather
// than render blanks.
func (c *Catalogue) Ready() bool {
	return c != nil && (len(c.Messages) > 0 || len(c.Fallback) > 0)
}

// Render returns the sentence for one fault.
//
// code is carried only so an unknown key can still name itself. It never
// selects wording: the key does that, and the same key reads the same way
// whichever code delivered it.
func (c *Catalogue) Render(code, key string, details map[string]string) string {
	if !c.Ready() {
		return ""
	}
	message, ok := c.lookup(key)
	if !ok {
		return c.renderUnknown(code, key)
	}
	template, ok := c.selectTemplate(key, message, details)
	if !ok {
		return c.renderUnknown(code, key)
	}
	return c.substitute(key, template, details)
}

// Hint returns the sentence for a hint enum, or "" when the hint carries
// none. HintNone is silence by design, not a missing translation.
func (c *Catalogue) Hint(hint string) string {
	key := HintKey(hint)
	if key == "" || !c.Ready() {
		return ""
	}
	message, ok := c.lookup(key)
	if !ok {
		return ""
	}
	return message.Text
}

// HintKey maps a hint enum onto its message key. The informal RETRY alias
// shares the RETRY_LOCAL sentence, which is what the dictionary says it means.
func HintKey(hint string) string {
	switch errcat.Hint(strings.TrimSpace(hint)) {
	case errcat.HintRetry, errcat.HintRetryLocal:
		return "hint.retry_local"
	case errcat.HintRetryBackoff:
		return "hint.retry_backoff"
	case errcat.HintRequireAction:
		return "hint.require_action"
	case errcat.HintAdminOnly:
		return "hint.admin_only"
	default:
		return ""
	}
}

func (c *Catalogue) lookup(key string) (Message, bool) {
	if message, ok := c.Messages[key]; ok {
		return message, true
	}
	message, ok := c.Fallback[key]
	return message, ok
}

// renderUnknown produces the catalogue's own sentence for a key nobody has
// written. If even that is missing, the code and key are still shown: a gap
// in the dictionary must never be rendered as silence.
func (c *Catalogue) renderUnknown(code, key string) string {
	message, ok := c.lookup(UnknownMessageKey)
	if !ok || message.Text == "" {
		return fmt.Sprintf("[%s %s]", code, key)
	}
	return placeholderPattern.ReplaceAllStringFunc(message.Text, func(match string) string {
		switch match {
		case "{code}":
			return code
		case "{key}":
			return key
		default:
			return match
		}
	})
}

// selectTemplate picks which wording applies.
//
// For a ladder that means the first rung whose every parameter arrived: the
// point of the ladder is that "somebody has this file" and "Anna has it until
// 13:41" are different sentences, not one sentence with holes.
func (c *Catalogue) selectTemplate(key string, message Message, details map[string]string) (string, bool) {
	switch {
	case message.Variants != nil:
		for _, variant := range message.Variants {
			if c.satisfied(key, variant, details) {
				return variant, true
			}
		}
		// The pack validator requires a parameterless last rung, so this is
		// only reachable for a malformed catalogue.
		return "", false
	case message.Plural != nil:
		// Selecting a plural category needs the reader's own language rules.
		// Go has none in the standard library, and hard-coding them here
		// would be the language branch this design exists to avoid, so the
		// Go side serves "other". No shipped pack uses a plural entry, and a
		// test fails the moment one does — see TestNoShippedPluralYet.
		if other, ok := message.Plural["other"]; ok {
			return other, true
		}
		return "", false
	default:
		if message.Text == "" {
			return "", false
		}
		return message.Text, true
	}
}

// satisfied reports whether every parameter a template names has a value.
func (c *Catalogue) satisfied(key, template string, details map[string]string) bool {
	for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		if _, ok := c.value(key, match[1], details); !ok {
			return false
		}
	}
	return true
}

// substitute fills a template. A parameter without a value keeps its
// placeholder rather than leaving a gap that reads like a broken sentence.
func (c *Catalogue) substitute(key, template string, details map[string]string) string {
	return placeholderPattern.ReplaceAllStringFunc(template, func(match string) string {
		name := match[1 : len(match)-1]
		value, ok := c.value(key, name, details)
		if !ok {
			return match
		}
		return value
	})
}

// value formats one declared argument for the reader.
//
// An undeclared Details key is diagnostic context, not an argument, and so is
// anything declared as diagnostic: unbounded, untranslated text has no place
// inside a translated sentence. Both are refused here as well as in the pack
// validator, because this renderer also serves catalogues that arrived over
// IPC from a daemon of a different build.
func (c *Catalogue) value(key, name string, details map[string]string) (string, bool) {
	raw, present := details[name]
	if !present || strings.TrimSpace(raw) == "" {
		return "", false
	}
	kind, declared := c.kind(key, name)
	if !declared || kind == errcat.ParamDiagnostic {
		return "", false
	}
	switch kind {
	case errcat.ParamTimestamp:
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", false
		}
		// Local wall-clock time, matching what the hand-written sentence
		// showed before the catalogue existed.
		return parsed.Local().Format("15:04"), true
	case errcat.ParamBytes:
		size, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return "", false
		}
		return FormatBytes(size), true
	case errcat.ParamNumber:
		if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
			return "", false
		}
		return raw, true
	default:
		// text, path and identifier are literal. A name, a path and an ID
		// read the same in every language and are never reformatted.
		return raw, true
	}
}

func (c *Catalogue) kind(key, name string) (errcat.ParamKind, bool) {
	for _, param := range c.Params[key] {
		if param.Name == name {
			return param.Kind, true
		}
	}
	return "", false
}

// FormatBytes renders a byte count for a reader.
func FormatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for scaled := value / unit; scaled >= unit; scaled /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}
