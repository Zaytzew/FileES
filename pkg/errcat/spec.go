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

// Spec is one dictionary entry. Identity is the (Code, Key) pair: a code
// may serve more than one key (LOCK-2001, REPO-2010, REALM-0001/1001),
// and protoErr historically reused PROTO-0001 for several keys.
type Spec struct {
	Code     Code
	Key      Key
	Severity Severity
	Hint     Hint
	// Fields are the Details keys a renderer may read for this Key.
	// Unknown keys in a Details map are diagnostic only.
	Fields []string
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
