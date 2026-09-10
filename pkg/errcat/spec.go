// Package errcat is the shared FileES error dictionary.
//
// It owns codes, message keys, severity, hints and presentation. It has no
// I/O, no SVN and no IPC. Daemon, GUI, CLI and other protocols import it;
// they do not invent parallel codes or sentences.
//
// Wire contracts stay in their own packages. This package is the vocabulary
// those contracts quote.
package errcat

import "strings"

// Code is the stable NS-NNNN identifier carried on every protocol.
type Code string

// Key is the dotted message key. GUI and CLI render from the key, never by
// parsing Details or by matching Code text.
type Key string

// Severity of a classified fault.
type Severity string

const (
	SevInfo  Severity = "INFO"
	SevWarn  Severity = "WARN"
	SevError Severity = "ERROR"
	SevFatal Severity = "FATAL"
)

// Hint tells the caller what kind of next step is appropriate.
//
// RETRY is an informal alias that leaked into ipcserver; it is catalogued
// so existing envelopes stay valid. New call sites should use RETRY_LOCAL
// or RETRY_BACKOFF.
type Hint string

const (
	HintNone          Hint = "NONE"
	HintRetry         Hint = "RETRY"
	HintRetryLocal    Hint = "RETRY_LOCAL"
	HintRetryBackoff  Hint = "RETRY_BACKOFF"
	HintRequireAction Hint = "REQUIRE_ACTION"
	HintAdminOnly     Hint = "ADMIN_ONLY"
)

// ParamKind is what a Details value means, and therefore what a renderer is
// allowed to do with it.
//
// The kind is the message schema, so it belongs to the dictionary and not to
// a translation. A language pack may move a parameter inside a sentence; it
// may not change what the value is. Formatting is the renderer's job — the
// daemon never pre-formats a number, a size or a date, because the reader's
// locale is not the daemon's to know.
type ParamKind string

const (
	// ParamText is literal text authored by a person: a holder, a server
	// name, a comment. Never translated, never reformatted.
	ParamText ParamKind = "text"
	// ParamPath is a filesystem or repository path. Literal, and never
	// split or shortened by the catalogue.
	ParamPath ParamKind = "path"
	// ParamIdentifier is a stable ID (repo, server, operation). Literal,
	// and identical in every language, because decisions travel by ID.
	ParamIdentifier ParamKind = "identifier"
	// ParamNumber is a count or position the renderer formats for its
	// locale, and the only kind a plural category may be selected from.
	ParamNumber ParamKind = "number"
	// ParamBytes is a byte count the renderer formats as a size.
	ParamBytes ParamKind = "bytes"
	// ParamTimestamp is an RFC3339 UTC instant the renderer formats in the
	// reader's zone. The daemon does not send "13:41".
	ParamTimestamp ParamKind = "timestamp"
	// ParamDiagnostic is raw diagnostic text — usually the "detail" field.
	//
	// A renderer may show it, deliberately and marked as diagnostics, but a
	// language pack may NOT place it inside a sentence: it is untranslated,
	// unbounded, and frequently English. Making it a placeholder would put
	// an uncontrolled string in the middle of a translated sentence and
	// quietly turn the catalogue into a text-assembly engine.
	ParamDiagnostic ParamKind = "diagnostic"
)

// Field is one declared parameter of a message: the Details key a renderer
// may read, and what that value is.
type Field struct {
	Name string
	Kind ParamKind
}

// Spec is one dictionary entry. Identity is the (Code, Key) pair: a code
// may serve more than one key (LOCK-2001, REPO-2010, REALM-0001/1001),
// and protoErr historically reused PROTO-0001 for several keys.
type Spec struct {
	Code     Code
	Key      Key
	Severity Severity
	Hint     Hint
	// Fields are the Details keys a renderer may read for this Key, with
	// the kind of each value. Details keys that are not declared here are
	// diagnostic only: they are not parameters and not part of the schema.
	Fields []Field
	// Diagnostic is the English log sentence. It is not a UI string.
	Diagnostic string
	// Polish is the default user sentence. An empty Details map must
	// still produce a complete sentence; fields fill a more specific one.
	Polish string
}

func (s Spec) Zero() bool { return s.Key == "" && s.Code == "" }

// NormalizeHint maps informal aliases onto themselves for wire compatibility
// but reports whether the value is in the dictionary at all.
func NormalizeHint(raw string) (Hint, bool) {
	h := Hint(strings.TrimSpace(raw))
	switch h {
	case HintNone, HintRetry, HintRetryLocal, HintRetryBackoff, HintRequireAction, HintAdminOnly, "":
		return h, true
	default:
		return h, false
	}
}
