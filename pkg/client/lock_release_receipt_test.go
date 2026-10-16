package client

import "testing"

func TestReleaseReceiptBinding(t *testing.T) {
	for _, state := range []string{"released", "not-held"} {
		r := map[string]any{"released_lock": map[string]any{"path": "folder/doc", "token": "old", "state": state}}
		if _, err := parseReleasedLock(r, "folder/doc", "old"); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"path", "token", "state"} {
			v := r["released_lock"].(map[string]any)
			old := v[field]
			v[field] = "other"
			if _, err := parseReleasedLock(r, "folder/doc", "old"); err == nil {
				t.Fatal("accepted changed", field)
			}
			v[field] = old
		}
	}
	if _, err := parseReleasedLock(map[string]any{}, "doc", "old"); err == nil {
		t.Fatal("missing receipt accepted")
	}
}
