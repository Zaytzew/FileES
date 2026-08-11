package errmap

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// Severity of a classified error event.
type Severity string

const (
	SevInfo  Severity = "INFO"
	SevWarn  Severity = "WARN"
	SevError Severity = "ERROR"
	SevFatal Severity = "FATAL"
)

// Hint — action hint for the caller / UI.
type Hint string

const (
	HintNone          Hint = "NONE"
	HintRetryLocal    Hint = "RETRY_LOCAL"    // retry immediately from local state
	HintRetryBackoff  Hint = "RETRY_BACKOFF"  // retry after exponential backoff
	HintRequireAction Hint = "REQUIRE_ACTION" // user must intervene
	HintAdminOnly     Hint = "ADMIN_ONLY"     // admin/ops must intervene
)

// Code — canonical error code in NS-NNNN form.
type Code string

const (
	CodeNetUnreachable  Code = "NET-4007"    // network / connectivity failure
	CodeAuthFailed      Code = "AUTH-4102"   // authentication or authorization rejected
	CodeLockHeld        Code = "LOCK-2001"   // file locked by another user
	CodeCommitOutdated  Code = "COMMIT-3101" // WC out of date — update required before commit
	CodeCommitNoVCS     Code = "COMMIT-3102" // path not under version control
	CodeCommitFailed    Code = "COMMIT-3100" // generic commit failure
	CodeReconFlict      Code = "RECON-3002"  // conflict detected during update, local copy saved to !kolizje/
	CodePolicyDeferred  Code = "POLICY-2201" // editing-policy migration waiting on a clean working copy
	CodeUnknown         Code = "SYNC-0000"   // unclassified error
)

// Entry is a classified error ready for logging or UI display.
type Entry struct {
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	Hint     Hint     `json:"hint"`
	Msg      string   `json:"msg"`            // human-readable summary
	Details  string   `json:"details"`        // raw error text
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
		return Entry{Code: CodeNetUnreachable, Severity: SevWarn, Hint: HintRetryBackoff,
			Msg: "Network unreachable — retrying with backoff", Details: msg}
	case containsAny(low, authNeedles):
		return Entry{Code: CodeAuthFailed, Severity: SevError, Hint: HintAdminOnly,
			Msg: "Authentication failed — check credentials", Details: msg}
	case containsAny(low, lockNeedles):
		return Entry{Code: CodeLockHeld, Severity: SevError, Hint: HintRequireAction,
			Msg: "File locked by another user", Details: msg}
	case containsAny(low, outdatedNeedles):
		return Entry{Code: CodeCommitOutdated, Severity: SevWarn, Hint: HintRetryLocal,
			Msg: "Working copy out of date — update required before next commit", Details: msg}
	case containsAny(low, novcNeedles):
		return Entry{Code: CodeCommitNoVCS, Severity: SevWarn, Hint: HintRetryLocal,
			Msg: "Path not under version control", Details: msg}
	case containsAny(low, commitNeedles):
		return Entry{Code: CodeCommitFailed, Severity: SevError, Hint: HintRetryLocal,
			Msg: "Commit failed", Details: msg}
	default:
		return Entry{Code: CodeUnknown, Severity: SevError, Hint: HintRetryLocal,
			Msg: "Unexpected error", Details: msg}
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
	TS       string   `json:"ts"`
	Scope    string   `json:"scope,omitempty"`
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	Hint     Hint     `json:"hint"`
	Msg      string   `json:"msg"`
	Details  string   `json:"details,omitempty"`
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
