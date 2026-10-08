package commit

import (
	"bytes"
	"strings"
	"testing"
)

func TestRecoveryCacheProofBindsEveryFieldAndEntry(t *testing.T) {
	base := `{"schema":"filees.commit-cache/v2","entries":[{"rel":"a","op":"modified","first_seen":"2026-10-08T00:00:00Z","extra":{"x":1}},{"rel":"z","op":"deleted"}],"intent_receipts":[{"plan_id":"one","paths":["a","z"]},{"plan_id":"two"}],"future":9}`
	proof, err := RecoveryCacheProof([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]string{
		"operation":           strings.Replace(base, "modified", "added", 1),
		"path":                strings.Replace(base, `"rel":"a"`, `"rel":"b"`, 1),
		"time":                strings.Replace(base, "00:00:00", "00:00:01", 1),
		"entry field":         strings.Replace(base, `"x":1`, `"x":2`, 1),
		"outer field":         strings.Replace(base, `"future":9`, `"future":8`, 1),
		"receipt":             strings.Replace(base, "two", "three", 1),
		"receipt paths":       strings.Replace(base, `["a","z"]`, `["z","a"]`, 1),
		"receipt order":       strings.Replace(base, `[{"plan_id":"one","paths":["a","z"]},{"plan_id":"two"}]`, `[{"plan_id":"two"},{"plan_id":"one","paths":["a","z"]}]`, 1),
		"remove entry":        strings.Replace(base, `,{"rel":"z","op":"deleted"}`, "", 1),
		"duplicate entry":     strings.Replace(base, `{"rel":"z","op":"deleted"}`, `{"rel":"z","op":"deleted"},{"rel":"z","op":"deleted"}`, 1),
		"duplicate inner key": strings.Replace(base, `"op":"modified"`, `"op":"deleted","op":"modified"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := RecoveryCacheProof([]byte(changed))
			if err != nil || bytes.Equal(proof, got) {
				t.Fatal("real change lost from proof", err)
			}
		})
	}
	for _, bad := range []string{
		`{"schema":"filees.commit-cache/v2","entries":[],"entries":[]}`,
		`{"schema":"future","entries":[]}`,
		`{"schema":"filees.commit-cache/v2"}`,
		base + `{}`,
		`[{"rel":"broken"}`,
	} {
		if _, err := RecoveryCacheProof([]byte(bad)); err == nil {
			t.Fatalf("ambiguous/unsupported input normalized: %s", bad)
		}
	}
}

func TestRecoveryCacheProofAllPermutations(t *testing.T) {
	items := []string{`{"rel":"a","op":"modified"}`, `{"rel":"b","op":"deleted"}`, `{"rel":"c","extra":[1,2]}`}
	for _, wrapped := range []bool{false, true} {
		var first []byte
		for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
			raw := "[\n " + items[order[0]] + ",\n " + items[order[1]] + ",\n " + items[order[2]] + "\n]"
			if wrapped {
				raw = `{"schema":"filees.commit-cache/v2","entries":` + raw + `,"intent_receipts":[{"plan_id":"first"},{"plan_id":"last"}]}`
			}
			proof, err := RecoveryCacheProof([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if first == nil {
				first = proof
			} else if !bytes.Equal(first, proof) {
				t.Fatal("order changed proof")
			}
		}
	}
}
