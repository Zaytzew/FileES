package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeAcquireUsesOneMutationAndRedactsReceipt(t *testing.T) {
	for _, receipt := range []string{`,"acquired_lock":{"token":"secret-token","owner":"actor","comment":"intent"}`, ""} {
		c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"locked":[{"path":"a","ok":true,"error":null`+receipt+`}]}`)
		if !nativeWCOps(c) {
			t.Skip("native WC operations unavailable")
		}
		trace := filepath.Join(t.TempDir(), "trace")
		t.Setenv("FILEES_TEST_RA_TRACE", trace)
		proof, out, err := c.AcquireLock(context.Background(), t.TempDir(), "a", "intent")
		if err != nil || (proof != nil) != (receipt != "") || strings.Contains(out, "secret-token") {
			t.Fatal("receipt handling", err)
		}
		calls, err := os.ReadFile(trace)
		// One local WC preflight and one remote mutation, no remote status.
		if err != nil || strings.Count(string(calls), "\n") != 2 || !strings.Contains(string(calls), `"--inspect-wc"`) || !strings.Contains(string(calls), `"lock"`) || strings.Contains(string(calls), `"status"`) {
			t.Fatal("unexpected extra server read", string(calls), err)
		}
	}
}

func TestAcquiredLockReceiptValidationAndRedaction(t *testing.T) {
	makeRow := func() map[string]any {
		return map[string]any{"path": "a", "ok": true, "error": nil, "acquired_lock": map[string]any{"token": "secret-token", "owner": "actor", "comment": "intent"}}
	}
	row := makeRow()
	rows, err := nativeLockReceipt(map[string]any{"locked": []any{row}}, "lock", []string{"a"})
	if err != nil || rows[0].Acquired == nil || rows[0].Acquired.Token != "secret-token" {
		t.Fatal(rows, err)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "intent") {
		t.Fatal("receipt leaked in output")
	}
	for _, change := range []func(map[string]any){
		func(r map[string]any) { r["path"] = "b" },
		func(r map[string]any) { r["ok"] = false; r["error"] = "denied" },
		func(r map[string]any) { r["acquired_lock"] = nil },
		func(r map[string]any) { r["acquired_lock"].(map[string]any)["token"] = "" },
		func(r map[string]any) { r["acquired_lock"].(map[string]any)["owner"] = "" },
		func(r map[string]any) { delete(r["acquired_lock"].(map[string]any), "comment") },
	} {
		r := makeRow()
		change(r)
		if _, err := nativeLockReceipt(map[string]any{"locked": []any{r}}, "lock", []string{"a"}); err == nil {
			t.Fatal("accepted invalid receipt")
		}
	}
	if _, err := nativeLockReceipt(map[string]any{"unlocked": []any{makeRow()}}, "unlock", []string{"a"}); err == nil {
		t.Fatal("accepted unlock receipt")
	}
	delete(row, "acquired_lock")
	rows, err = nativeLockReceipt(map[string]any{"locked": []any{row}}, "lock", []string{"a"})
	if err != nil || rows[0].Acquired != nil {
		t.Fatal("legacy should require reconciliation", err)
	}
}
