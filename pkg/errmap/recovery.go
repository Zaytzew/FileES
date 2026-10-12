package errmap

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"filees/pkg/errcat"
)

// RecoveryTransactionID accepts only the bounded diagnostic emitted by commit
// recovery, not an arbitrary occurrence of transaction= in an error's cause.
func RecoveryTransactionID(key errcat.Key, details string) string {
	prefix := "commit recovery HOLD: transaction="
	if key == errcat.KeyCommitRecoveryResolved {
		prefix = "commit recovery confirmed: transaction="
	} else if key != errcat.KeyCommitRecoveryHeld {
		return ""
	}
	if !strings.HasPrefix(details, prefix) {
		return ""
	}
	id, _, ok := strings.Cut(strings.TrimPrefix(details, prefix), " ")
	if !ok || id == "" || len(id) > 128 || id == "unavailable" {
		return ""
	}
	for _, r := range id {
		if r != '-' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return id
}

// RecoveryPending reads history after restart; an earlier confirmation cannot
// close a later HOLD. A missing log is not a pending diagnostic. Other read
// failures are returned, never interpreted as confirmation.
func RecoveryPending(path, id string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 128*1024)
	pending := false
	for sc.Scan() {
		var row jsonLine
		if json.Unmarshal(sc.Bytes(), &row) != nil || !strings.HasPrefix(row.Scope, "commit:") || RecoveryTransactionID(row.Key, row.Details) != id || id == "" {
			continue
		}
		pending = row.Key == errcat.KeyCommitRecoveryHeld
	}
	return pending, sc.Err()
}

// ResolveRecoveryLines projects history without rewriting the append-only log.
// Match both scope and transaction; only a later successful inspection closes
// a HOLD. Original timestamp/diagnostics remain available to the journal.
func ResolveRecoveryLines(lines []string) []string {
	out := append([]string(nil), lines...)
	confirmed := map[string]string{}
	spec, _ := errcat.ByKey(errcat.KeyCommitRecoveryResolved)
	for i := len(out) - 1; i >= 0; i-- {
		var row jsonLine
		if json.Unmarshal([]byte(out[i]), &row) != nil || !strings.HasPrefix(row.Scope, "commit:") {
			continue
		}
		id := RecoveryTransactionID(row.Key, row.Details)
		if id == "" {
			continue
		}
		binding := row.Scope + "\x00" + id
		if row.Key == errcat.KeyCommitRecoveryResolved {
			confirmed[binding] = row.TS
			continue
		}
		if at := confirmed[binding]; at != "" {
			row.Key, row.Code, row.Severity, row.Hint, row.Msg = spec.Key, spec.Code, spec.Severity, spec.Hint, spec.Diagnostic
			row.Details += "\nRecovery confirmed at " + at
			if raw, err := json.Marshal(row); err == nil {
				out[i] = string(raw)
			}
		}
	}
	return out
}
