package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/state"
	"filees/pkg/serverconfig"
)

func serverToolchainManifest() *manifest.Manifest {
	return &manifest.Manifest{Sequence: serverConfigV2FirstSequence, Files: []manifest.File{{
		Target: "{libexec_dir}/filees/filees-public-authority",
	}}}
}

func TestServerConfigV1MigrationAddsPresentationIdentity(t *testing.T) {
	r, _ := testRunner(t)
	if err := os.MkdirAll(r.Config.SysconfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.Config.SysconfDir, "server.json")
	before := `{
  "schema": "filees.server-toolchain/v1",
  "root": "/var/filees/onboarding",
  "invitation": {"server_id": "atmprojekt:filees", "server_address": "example:22"},
  "private_extension": {"kept": true}
}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	migration, err := r.planServerConfigMigration(serverToolchainManifest())
	if err != nil {
		t.Fatal(err)
	}
	if migration == nil || migration.FromSchema != serverConfigV1 || migration.ToSchema != serverconfig.Schema {
		t.Fatalf("unexpected migration: %+v", migration)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(migration.Data, &got); err != nil {
		t.Fatal(err)
	}
	var schema, displayName string
	if err := json.Unmarshal(got["schema"], &schema); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got["display_name"], &displayName); err != nil {
		t.Fatal(err)
	}
	if schema != serverconfig.Schema || displayName != "atmprojekt:filees" {
		t.Fatalf("identity = schema %q display_name %q", schema, displayName)
	}
	if _, ok := got["private_extension"]; !ok {
		t.Fatal("migration discarded an unrelated configuration field")
	}
}

func TestServerConfigMigrationTargetComesFromCompatibleManifestFields(t *testing.T) {
	m := serverToolchainManifest()
	m.Sequence = 1
	m.Configs = []manifest.ConfigContract{{
		Name: "server", Path: "/etc/filees/server.json",
		DefaultChanged: []manifest.DefaultChange{{Key: "schema", Old: serverConfigV1, New: serverconfig.Schema}},
	}}
	got, err := targetServerConfigSchema(m)
	if err != nil || got != serverconfig.Schema {
		t.Fatalf("target schema = %q, %v", got, err)
	}

	m.Configs = nil
	got, err = targetServerConfigSchema(m)
	if err != nil || got != serverConfigV1 {
		t.Fatalf("historical target schema = %q, %v", got, err)
	}
}

func TestServerConfigMigrationIsStrictAndIdempotent(t *testing.T) {
	r, _ := testRunner(t)
	if err := os.MkdirAll(r.Config.SysconfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.Config.SysconfDir, "server.json")
	current := `{"schema":"filees.server-toolchain/v2","display_name":"Cloud ATM Projekt"}`
	if err := os.WriteFile(path, []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	migration, err := r.planServerConfigMigration(serverToolchainManifest())
	if err != nil || migration != nil {
		t.Fatalf("current config planned migration: %+v, %v", migration, err)
	}

	if err := os.WriteFile(path, []byte(`{"schema":"filees.server-toolchain/v0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.planServerConfigMigration(serverToolchainManifest()); err == nil || !strings.Contains(err.Error(), "no server configuration migration") {
		t.Fatalf("unknown generation was accepted: %v", err)
	}
}

func TestConfigMigrationParticipatesInInstallRollback(t *testing.T) {
	r, _ := testRunner(t)
	path := filepath.Join(r.Config.SysconfDir, "server.json")
	stage := filepath.Join(r.Config.StageDir, "server.json")
	for file, data := range map[string][]byte{path: []byte("v1\n"), stage: []byte("v2\n")} {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	ownership := r.Ownership.(fakeOwnership).value
	entry, err := r.installStaged([]StagedFile{{Target: path, StagePath: stage, Mode: info.Mode().Perm(), Ownership: ownership}}, &state.State{}, "r2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v2\n" {
		t.Fatalf("migration was not installed: %q", got)
	}
	if err := r.restoreEntry(entry); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v1\n" {
		t.Fatalf("rollback did not restore v1 config: %q", got)
	}
}
