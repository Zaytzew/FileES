// Package domaincatalog holds the daemon's domain language packs.
//
// The daemon owns what an event means: its code, severity, hint, available
// actions and the sentence a person reads. pkg/errcat stays the identity of
// an error — a language pack adds no codes, changes no severity and grants
// no actions. It only says how one known message reads in one language.
//
// A pack is data compiled into the binary, not a plugin: no DLL, no
// JavaScript, nothing loaded from the disk of a running daemon. The build
// discovers catalogs/*.json, validates them and embeds them, so adding a
// language is a reviewed file plus a rebuild rather than a branch in the
// logic. The renderer formats numbers, sizes and dates for its reader; the
// daemon never sends a pre-formatted "13:41".
package domaincatalog

import (
	"sort"

	"filees/pkg/errcat"
)

// Schema is the pack format this build understands. A pack declaring
// anything else is rejected rather than read optimistically.
const Schema = "filees.domain-catalog/v1"

// BaseLocale is the full base catalogue. Every other pack is checked for
// completeness against the same key set, so a fallback is a safety net for
// an unsupported reader locale, not a licence to ship a half-translated pack.
const BaseLocale = "en"

// KeySchema is everything a language pack is allowed to say about one
// message: which key, and which parameters that message may place.
type KeySchema struct {
	Key    string
	Params []errcat.Field
}

// Param returns the declared parameter by name.
func (s KeySchema) Param(name string) (errcat.Field, bool) {
	for _, field := range s.Params {
		if field.Name == name {
			return field, true
		}
	}
	return errcat.Field{}, false
}

// metaSchemas are the catalogue's own messages. They are not faults, so they
// have no code in errcat, but they are read by a person and therefore belong
// in the same pack rather than in a second dictionary somewhere else.
//
// Hints are here because the daemon owns the hint enum: HintNone deliberately
// has no sentence, and the informal RETRY alias shares hint.retry_local, which
// is what pkg/errcat's own documentation says it means.
var metaSchemas = []KeySchema{
	{Key: KeyUnknownMessage, Params: []errcat.Field{
		{Name: "code", Kind: errcat.ParamIdentifier},
		{Name: "key", Kind: errcat.ParamIdentifier},
	}},
	{Key: KeyHintRetryLocal},
	{Key: KeyHintRetryBackoff},
	{Key: KeyHintRequireAction},
	{Key: KeyHintAdminOnly},
}

// Catalogue-owned message keys.
const (
	// KeyUnknownMessage is shown when a key is missing from every pack. It
	// names the code and the key on purpose: an unknown fault must stay
	// visible as a gap in the dictionary, not be smoothed into "an error
	// occurred" that nobody can act on or report.
	KeyUnknownMessage = "catalog.unknown_message"

	KeyHintRetryLocal    = "hint.retry_local"
	KeyHintRetryBackoff  = "hint.retry_backoff"
	KeyHintRequireAction = "hint.require_action"
	KeyHintAdminOnly     = "hint.admin_only"
)

// HintKey returns the message key for a hint, or "" when the hint carries no
// sentence. HintNone is silence by design, not a missing translation.
func HintKey(hint errcat.Hint) string {
	switch hint {
	case errcat.HintRetry, errcat.HintRetryLocal:
		return KeyHintRetryLocal
	case errcat.HintRetryBackoff:
		return KeyHintRetryBackoff
	case errcat.HintRequireAction:
		return KeyHintRequireAction
	case errcat.HintAdminOnly:
		return KeyHintAdminOnly
	default:
		return ""
	}
}

// Schemas returns every key a pack may carry, keyed by message key.
//
// Where one key is served by more than one wire code (PROTO-0001 alongside
// PROTO-0004/0005), the preferred spec decides the parameters: the code is
// the identity on the wire, the key is the identity of the sentence.
func Schemas() map[string]KeySchema {
	out := make(map[string]KeySchema, len(errcat.All())+len(metaSchemas))
	for _, spec := range errcat.All() {
		preferred, ok := errcat.ByKey(spec.Key)
		if !ok || preferred.Code != spec.Code {
			continue
		}
		out[string(spec.Key)] = KeySchema{Key: string(spec.Key), Params: spec.Fields}
	}
	for _, meta := range metaSchemas {
		out[meta.Key] = meta
	}
	return out
}

// SchemaKeys returns every declared key in a stable order.
func SchemaKeys() []string {
	schemas := Schemas()
	keys := make([]string, 0, len(schemas))
	for key := range schemas {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
