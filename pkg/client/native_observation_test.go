package client

import (
	"encoding/json"
	"testing"
)

func TestNativeInfoRequiresExplicitRevisionEvidence(t *testing.T) {
	const valid = `{"entries":[{"path":".","url":"file:///repo","repos_root_url":"file:///repo","repos_uuid":"uuid","kind":"dir","revision":0,"last_changed_rev":0}]}`
	for _, field := range []string{"revision", "last_changed_rev"} {
		for _, bad := range []any{nil, "0", -1.0, 1.5, 9007199254740992.0} {
			var raw map[string]any
			_ = json.Unmarshal([]byte(valid), &raw)
			if _, err := parseNativeInfo(raw); err != nil {
				t.Fatal(err)
			}
			row := raw["entries"].([]any)[0].(map[string]any)
			row[field] = bad
			if _, err := parseNativeInfo(raw); err == nil {
				t.Fatalf("accepted %s=%v", field, bad)
			}
			delete(row, field)
			if _, err := parseNativeInfo(raw); err == nil {
				t.Fatalf("accepted absent %s", field)
			}
		}
	}
}

func TestNativeLockObservationRequiresRemoteEvidence(t *testing.T) {
	const valid = `{"remote":true,"against_revision":1,"entries":[{"path":"a.txt","item":"normal","props":"none","local_lock":{"token":"stale"},"repos_lock":null}]}`
	read := func() map[string]any { var raw map[string]any; _ = json.Unmarshal([]byte(valid), &raw); return raw }
	if locks, err := parseNativeLockObservations(read(), "a.txt"); err != nil || len(locks) != 0 {
		t.Fatalf("stale local token: %v %v", locks, err)
	}
	for _, mutate := range []func(map[string]any){
		func(r map[string]any) { delete(r, "remote") },
		func(r map[string]any) { delete(r, "against_revision") },
		func(r map[string]any) { r["against_revision"] = nil },
		func(r map[string]any) { r["remote"] = false },
		func(r map[string]any) { delete(r, "entries") },
		func(r map[string]any) { r["entries"] = []any{} },
		func(r map[string]any) { delete(r["entries"].([]any)[0].(map[string]any), "repos_lock") },
		func(r map[string]any) {
			r["entries"].([]any)[0].(map[string]any)["repos_lock"] = map[string]any{"token": "incomplete"}
		},
		func(r map[string]any) { r["entries"].([]any)[0].(map[string]any)["path"] = "other.txt" },
		func(r map[string]any) { r["entries"] = append(r["entries"].([]any), r["entries"].([]any)[0]) },
	} {
		raw := read()
		mutate(raw)
		if _, err := parseNativeLockObservations(raw, "a.txt"); err == nil {
			t.Fatalf("accepted incomplete evidence: %#v", raw)
		}
	}
}
