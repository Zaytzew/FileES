package errmap

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"filees/pkg/errcat"
)

// Severity of a classified error event. Values are owned by pkg/errcat.
type Severity = errcat.Severity

const (
	SevInfo  = errcat.SevInfo
	SevWarn  = errcat.SevWarn
	SevError = errcat.SevError
	SevFatal = errcat.SevFatal
)

// Hint — action hint for the caller / UI. Values are owned by pkg/errcat.
type Hint = errcat.Hint

const (
	HintNone          = errcat.HintNone
	HintRetryLocal    = errcat.HintRetryLocal
	HintRetryBackoff  = errcat.HintRetryBackoff
	HintRequireAction = errcat.HintRequireAction
	HintAdminOnly     = errcat.HintAdminOnly
)

// Code — canonical error code in NS-NNNN form. Values are owned by pkg/errcat.
type Code = errcat.Code

const (
	CodeNetUnreachable = errcat.CodeNet
	CodeAuthFailed     = errcat.CodeAuth
	CodeLockHeld       = errcat.CodeLockHeld
	CodeCommitOutdated = errcat.CodeCommitStale
	CodeCommitNoVCS    = errcat.CodeCommitNoVCS
	CodeCommitFailed   = errcat.CodeCommitFail
	CodeReconFlict     = errcat.CodeRecon
	CodePolicyDeferred = errcat.CodePolicyWait
	CodeUnknown        = errcat.CodeUnknown
)

// Entry is a classified error ready for logging or UI display.
type Entry struct {
	Code     Code       `json:"code"`
	Key      errcat.Key `json:"key,omitempty"`
	Severity Severity   `json:"severity"`
	Hint     Hint       `json:"hint"`
	Msg      string     `json:"msg"`     // English diagnostic from the catalog
	Details  string     `json:"details"` // raw error text
}

func entryFrom(key errcat.Key, details string) Entry {
	spec, ok := errcat.ByKey(key)
	if !ok {
		spec, _ = errcat.ByKey(errcat.KeyUnknown)
	}
	return Entry{Code: spec.Code, Key: spec.Key, Severity: spec.Severity, Hint: spec.Hint, Msg: spec.Diagnostic, Details: details}
}

// IsNoop reports whether entry is empty (Classify was called with nil).
func (e Entry) IsNoop() bool { return e.Code == "" }

// IsNetwork reports whether the entry represents a network/connectivity error.
func (e Entry) IsNetwork() bool { return e.Code == CodeNetUnreachable }

// Classify maps a Go error to a structured Entry.
// Returns a zero Entry (IsNoop() == true) when err is nil.
func Classify(err error) Entry {
	if err == nil {
		return Entry{}
	}
	msg := err.Error()
	low := strings.ToLower(msg)

	switch {
	case containsAny(low, netNeedles):
		return entryFrom(errcat.KeyNetUnreachable, msg)
	case containsAny(low, authNeedles):
		return entryFrom(errcat.KeyAuthFailed, msg)
	case containsAny(low, lockNeedles):
		return entryFrom(errcat.KeyLockHeldByOther, msg)
	case containsAny(low, outdatedNeedles):
		return entryFrom(errcat.KeyCommitOutdated, msg)
	case containsAny(low, novcNeedles):
		return entryFrom(errcat.KeyCommitNoVCS, msg)
	case containsAny(low, commitNeedles):
		return entryFrom(errcat.KeyCommitFailed, msg)
	default:
		return entryFrom(errcat.KeyUnknown, msg)
	}
}

// Pre-built: matches same patterns as client.IsNetworkError plus common SVN E-codes.
var (
	netNeedles = []string{
		"unable to connect", "connection refused", "connection timed out",
		"network is unreachable", "no route to host", "host not found",
		"name or service not known", "temporary failure in name resolution",
		"e170013", "e730047",
		"anulowana/przekroczono czas", // timeout wrapper from client.go
	}
	authNeedles = []string{
		"authorization failed", "authentication failed",
		"e215004", "e170001",
		"password incorrect", "wrong password", "no matching host key",
	}
	lockNeedles = []string{
		"is already locked", "locked by",
		"e200015",
	}
	outdatedNeedles = []string{
		"out of date", "e160028", "needs to be updated",
	}
	novcNeedles = []string{
		"is not under version control", "not versioned",
		"e200009", "e150002",
	}
	commitNeedles = []string{
		"commit failed", "cannot commit",
	}
)

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// --- Sink ---

// Sink writes classified errors as JSON Lines (one per line) to an io.Writer.
// Nil Sink is safe to use — Emit is a no-op.
type Sink struct {
	mu    sync.Mutex
	out   io.Writer
	scope string // e.g. "commit:repoA"
}

// jsonLine is the JSON Lines schema for one error event.
type jsonLine struct {
	TS       string     `json:"ts"`
	Scope    string     `json:"scope,omitempty"`
	Code     Code       `json:"code"`
	Key      errcat.Key `json:"key,omitempty"`
	Severity Severity   `json:"severity"`
	Hint     Hint       `json:"hint"`
	Msg      string     `json:"msg"`
	Details  string     `json:"details,omitempty"`
}

// NewSink creates a Sink that writes to w with the given scope label.
func NewSink(w io.Writer, scope string) *Sink {
	return &Sink{out: w, scope: scope}
}

// Emit writes entry as a JSON Line. A nil Sink or a noop Entry is silently ignored.
func (s *Sink) Emit(entry Entry) {
	s.EmitAt(time.Now(), entry)
}

// EmitAt writes entry as a JSON Line using an explicit timestamp. Callers
// that need to correlate this entry with the deterministic error.list ID
// (`ts + ":" + code`, see pkg/ipcserver/handlers.go's parseErrLine) must use
// the same ts value they pass here to compute that ID themselves.
func (s *Sink) EmitAt(ts time.Time, entry Entry) {
	if s == nil || entry.IsNoop() {
		return
	}
	rec := jsonLine{
		TS:       ts.UTC().Format(time.RFC3339),
		Scope:    s.scope,
		Code:     entry.Code,
		Key:      entry.Key,
		Severity: entry.Severity,
		Hint:     entry.Hint,
		Msg:      entry.Msg,
		Details:  entry.Details,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b = append(b, '\n')
	s.mu.Lock()
	_, _ = s.out.Write(b)
	s.mu.Unlock()
}
