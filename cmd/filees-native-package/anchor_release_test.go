package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConditionalRuntimeSourcesMustBothMatch(t *testing.T) {
	for _, scenario := range []string{"ordinary", "required-missing", "complete", "missing-transform", "stale-include", "stale-transform"} {
		t.Run(scenario, func(t *testing.T) {
			source, stage := t.TempDir(), t.TempDir()
			current := filepath.Join(source, "native", "subversion-filees")
			notices := filepath.Join(stage, "notices")
			for _, dir := range []string{current, notices} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, pair := range [][2]string{{"filees_release.inc", "FileES-SVN-modification.inc"}, {"apply-runtime.cjs", "FileES-SVN-transformation.cjs"}} {
				body := []byte(pair[0])
				if err := os.WriteFile(filepath.Join(current, pair[0]), body, 0600); err != nil {
					t.Fatal(err)
				}
				if scenario == "ordinary" || scenario == "required-missing" || (scenario == "missing-transform" && pair[0] == "apply-runtime.cjs") {
					continue
				}
				if (scenario == "stale-include" && pair[0] == "filees_release.inc") || (scenario == "stale-transform" && pair[0] == "apply-runtime.cjs") {
					body = []byte("old source")
				}
				if err := os.WriteFile(filepath.Join(notices, pair[1]), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyConditionalSources(source, stage, scenario != "ordinary")
			wantOK := scenario == "ordinary" || scenario == "complete"
			if (err == nil) != wantOK {
				t.Fatalf("err=%v, want OK=%v", err, wantOK)
			}
		})
	}
}

func TestAnchorReleaseReceiptRequiresSchemaVersionAndAllFeatures(t *testing.T) {
	for _, scenario := range []string{"valid", "old-helper", "wrong-schema", "wrong-version", "not-ok", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			v := map[string]any{"ok": true, "schema": "filees.cfapi/v1", "version": "0.1.18", "features": []string{"prepare_release_bound_v1", "restore_release_v1", "release_observation_v1", "remote_tree_v1"}}
			switch scenario {
			case "old-helper":
				v["features"] = []string{"restore_release_v1"}
			case "wrong-schema":
				v["schema"] = "another"
			case "wrong-version":
				v["version"] = "0.1.17"
			case "not-ok":
				v["ok"] = false
			}
			raw, _ := json.Marshal(v)
			if scenario == "malformed" {
				raw = []byte("not json")
			}
			err := verifyAnchorReleaseReceipt(raw, "filees.cfapi/v1", "0.1.18", "prepare_release_bound_v1", "restore_release_v1", "release_observation_v1", "remote_tree_v1")
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
