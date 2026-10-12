package errmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/errcat"
)

func recoveryLine(key errcat.Key, scope, id string) string {
	detail := "commit recovery HOLD: transaction=" + id + " phase=done revision=154"
	if key == errcat.KeyCommitRecoveryResolved {
		detail = "commit recovery confirmed: transaction=" + id + " revision=154"
	}
	spec, _ := errcat.ByKey(key)
	b, _ := json.Marshal(jsonLine{TS: "2026-10-12T15:14:30Z", Scope: scope, Code: spec.Code, Key: key, Hint: spec.Hint, Severity: spec.Severity, Msg: spec.Diagnostic, Details: detail})
	return string(b)
}

func TestRecoveryProjectionMatchesExactHistory(t *testing.T) {
	lines := []string{recoveryLine(errcat.KeyCommitRecoveryHeld, "commit:a", "old"),
		recoveryLine(errcat.KeyCommitRecoveryHeld, "commit:b", "old"),
		recoveryLine(errcat.KeyCommitRecoveryHeld, "commit:a", "different"),
		recoveryLine(errcat.KeyCommitRecoveryResolved, "commit:a", "old"),
		recoveryLine(errcat.KeyCommitRecoveryHeld, "commit:a", "old"), "{broken"}
	out := ResolveRecoveryLines(lines)
	for i, key := range []errcat.Key{errcat.KeyCommitRecoveryResolved, errcat.KeyCommitRecoveryHeld, errcat.KeyCommitRecoveryHeld, errcat.KeyCommitRecoveryResolved, errcat.KeyCommitRecoveryHeld} {
		var row jsonLine
		if err := json.Unmarshal([]byte(out[i]), &row); err != nil || row.Key != key {
			t.Fatal(i, row, err)
		}
		if i == 0 && (row.Hint != HintNone || row.Severity != SevInfo || !strings.Contains(row.Details, "phase=done")) {
			t.Fatal("history lost or still actionable", row)
		}
	}
	if lines[0] == out[0] || out[5] != lines[5] || !strings.Contains(lines[0], string(errcat.KeyCommitRecoveryHeld)) {
		t.Fatal("input changed")
	}
}

func TestRecoveryPendingSurvivesRestartAndNewHold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	lines := []string{recoveryLine(errcat.KeyCommitRecoveryHeld, "commit:a", "old"), recoveryLine(errcat.KeyCommitRecoveryResolved, "commit:a", "other"), recoveryLine(errcat.KeyCommitRecoveryResolved, "commit:a", "old"), recoveryLine(errcat.KeyCommitRecoveryHeld, "commit:a", "old")}
	for i, want := range []bool{true, true, false, true} {
		if err := os.WriteFile(path, []byte(strings.Join(lines[:i+1], "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := RecoveryPending(path, "old"); err != nil || got != want {
			t.Fatal(i, got, err)
		}
	}
	if pending, err := RecoveryPending(path+"-missing", "old"); pending || err != nil {
		t.Fatal(pending, err)
	}
	if _, err := RecoveryPending(filepath.Dir(path), "old"); err == nil {
		t.Fatal("read failure hidden")
	}
	for _, value := range []string{"cause=transaction=old", "commit recovery HOLD: transaction=old", "commit recovery HOLD: transaction=unavailable phase=done", "commit recovery HOLD: transaction=old/other phase=done"} {
		if RecoveryTransactionID(errcat.KeyCommitRecoveryHeld, value) != "" {
			t.Fatal("unbound ID accepted", value)
		}
	}
}
