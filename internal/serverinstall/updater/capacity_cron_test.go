//go:build !windows

package updater

import (
	"context"
	"encoding/json"
	"filees/internal/serverinstall/cronjob"
	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapacityCronOnlyForSignedMonitorRelease(t *testing.T) {
	r, _ := testRunner(t)
	before := prepareCapacityCron
	defer func() { prepareCapacityCron = before }()
	calls := 0
	prepareCapacityCron = func(_ context.Context, admin, config string, first bool) (*cronjob.Plan, error) {
		calls++
		return &cronjob.Plan{Changed: true}, nil
	}
	m := &manifest.Manifest{Files: []manifest.File{{Target: "{sbin_dir}/filees-admin"}}}
	p, e := r.capacityCron(context.Background(), m, false)
	if e != nil || p != nil || calls != 0 {
		t.Fatal(p, e, calls)
	}
	m.Files = append(m.Files, manifest.File{Target: "{sysconf_dir}/capacity-alerts.example.json"})
	p, e = r.capacityCron(context.Background(), m, false)
	if e != nil || p == nil || !p.Changed || calls != 1 {
		t.Fatal(p, e, calls)
	}
}

func TestApplyRepairsCapacityCronAndDryRunDoesNot(t *testing.T) {
	r, root := testRunner(t)
	r.Config.StageDir = filepath.Join(root, "stage")
	r.Config.BackupDir = filepath.Join(root, "backup")
	if err := state.Save(r.Config.StateDir, &state.State{InstalledRelease: "r1", HighestSequence: 1, SecurityEpoch: 1, System: &state.SystemState{Adopted: true}}); err != nil {
		t.Fatal(err)
	}
	table := filepath.Join(root, "crontab")
	if err := os.WriteFile(table, []byte("# unrelated job\n"), 0600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "fake-crontab")
	script := "#!/bin/sh\nif [ \"$3\" = -l ]; then cat '" + table + "'; else cat > '" + table + "'; fi\n"
	if err := os.WriteFile(program, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	old := prepareCapacityCron
	defer func() { prepareCapacityCron = old }()
	prepareCapacityCron = func(_ context.Context, admin, config string, _ bool) (*cronjob.Plan, error) {
		line, e := cronjob.Line(admin, config)
		return &cronjob.Plan{Program: program, Line: line, Changed: true}, e
	}
	payload := []byte("test payload\n")
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ReleaseID: "r2", Platform: r.Config.Platform, Sequence: 2, SecurityEpoch: 1, Files: []manifest.File{
		{Source: "bin/filees-admin", Target: "{sbin_dir}/filees-admin", Kind: "binary", Mode: "0555", Owner: "root", Group: "wheel", SHA256: sha256hex(payload)},
		{Source: "share/filees/capacity-alerts.example.json", Target: "{sysconf_dir}/capacity-alerts.example.json", Kind: "file", Mode: "0644", Owner: "root", Group: "wheel", SHA256: sha256hex(payload)},
	}}
	raw, _ := json.Marshal(m)
	manifestPath := manifest.ReleaseManifestPath("r2", r.Config.Platform)
	fetch := mapFetcher{manifestPath: raw}
	for _, f := range m.Files {
		fetch[filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), f.Source))] = payload
	}
	r.Fetcher = fetch
	if e := r.Apply(context.Background(), Options{ReleaseID: "r2", DryRun: true, Yes: true}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(table)
	if strings.Contains(string(before), cronjob.Marker) {
		t.Fatal("dry run changed cron")
	}
	if e := r.Apply(context.Background(), Options{ReleaseID: "r2", Yes: true}); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(table)
	if !strings.HasPrefix(string(after), "# unrelated job\n") || strings.Count(string(after), cronjob.Marker) != 1 {
		t.Fatal(string(after))
	}
	st, e := state.Load(r.Config.StateDir)
	if e != nil || st.InstalledRelease != "r2" {
		t.Fatal(st, e)
	}
}
