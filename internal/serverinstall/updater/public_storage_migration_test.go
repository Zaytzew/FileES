package updater

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/state"
)

func storageManifest() *manifest.Manifest {
	m := serverToolchainManifest()
	m.Configs = append(m.Configs, manifest.ConfigContract{Name: "public-download-storage", DefaultChanged: []manifest.DefaultChange{{Key: "layout", New: publicStorageLayout}}})
	return m
}
func writeStorageConfig(t *testing.T, r *Runner, name, raw string) string {
	t.Helper()
	if err := os.MkdirAll(r.Config.SysconfDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.Config.SysconfDir, name)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestPublicStorageMigrationIsExplicitSelectiveAndTransactional(t *testing.T) {
	r, root := testRunner(t)
	r.Config.PublicDownloadsDir = "/home/_filees"
	before := map[string]string{
		"server.json":       `{"schema":"filees.server-toolchain/v2","display_name":"Cloud","public_shares":{"enabled":true,"authority_staging_root":"/var/tmp/filees-public-share-authority","other":"keep"},"private_extension":{"kept":true}}`,
		"public-links.json": `{"schema":"filees.public-links/v1","cache":{"enabled":true,"root":"/var/tmp/filees-public-shares-cache","ttl":"12h","max_size":10737418240},"visit_key_file":"/etc/filees/keep.key"}`,
	}
	for name, raw := range before {
		writeStorageConfig(t, r, name, raw)
	}
	none, err := r.planConfigMigrations(serverToolchainManifest())
	if err != nil || len(none) != 0 {
		t.Fatalf("historical manifest changed paths: %+v %v", none, err)
	}
	migrations, err := r.planConfigMigrations(storageManifest())
	if err != nil || len(migrations) != 2 {
		t.Fatalf("plan: %+v %v", migrations, err)
	}
	var staged []StagedFile
	for _, m := range migrations {
		name := filepath.Base(m.Path)
		raw, _ := os.ReadFile(m.Path)
		if string(raw) != before[name] {
			t.Fatal("planning wrote configuration")
		}
		if !bytes.Contains(m.Data, []byte("/home/_filees/")) || len(m.Directories) != 1 {
			t.Fatalf("missing relocation: %s", m.Data)
		}
		var doc map[string]any
		if err := json.Unmarshal(m.Data, &doc); err != nil {
			t.Fatal(err)
		}
		if name == "server.json" && doc["private_extension"] == nil {
			t.Fatal("lost unknown field")
		}
		if name == "public-links.json" && doc["visit_key_file"] != "/etc/filees/keep.key" {
			t.Fatal("changed key")
		}
		stage := filepath.Join(root, name+".staged")
		if err := os.WriteFile(stage, m.Data, 0600); err != nil {
			t.Fatal(err)
		}
		staged = append(staged, StagedFile{Target: m.Path, StagePath: stage, Mode: 0600, Ownership: r.Ownership.(fakeOwnership).value})
	}
	entry, err := r.installStaged(staged, &state.State{}, "storage-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.planConfigMigrations(storageManifest())
	if err != nil || len(again) != 0 {
		t.Fatalf("not idempotent: %+v %v", again, err)
	}
	if err := r.restoreEntry(entry); err != nil {
		t.Fatal(err)
	}
	for name, want := range before {
		got, _ := os.ReadFile(filepath.Join(r.Config.SysconfDir, name))
		if string(got) != want {
			t.Fatalf("rollback changed %s", name)
		}
	}
}
func TestPublicStorageMigrationPreservesCustomPathsAndMergesSchema(t *testing.T) {
	r, _ := testRunner(t)
	r.Config.PublicDownloadsDir = "/home/_filees"
	writeStorageConfig(t, r, "server.json", `{"schema":"filees.server-toolchain/v1","invitation":{"server_id":"cloud"},"public_shares":{"enabled":true}}`)
	writeStorageConfig(t, r, "public-links.json", `{"schema":"filees.public-links/v1","cache":{"enabled":true,"root":"/data/custom-cache","max_size":10737418240}}`)
	migrations, err := r.planConfigMigrations(storageManifest())
	if err != nil || len(migrations) != 1 {
		t.Fatalf("%+v %v", migrations, err)
	}
	if !bytes.Contains(migrations[0].Data, []byte("display_name")) || !bytes.Contains(migrations[0].Data, []byte("/home/_filees/authority")) {
		t.Fatal("lost combined migration")
	}
	r.Config.PublicDownloadsDir = "/var/tmp/bad"
	if _, err := r.planConfigMigrations(storageManifest()); err == nil {
		t.Fatal("migration targets scratch")
	}
}

func TestPublicStorageMigrationPreservesBothIndependentCustomPaths(t *testing.T) {
	r, _ := testRunner(t)
	r.Config.PublicDownloadsDir = "/home/_filees"
	before := map[string]string{
		"server.json":       `{"schema":"filees.server-toolchain/v2","display_name":"Cloud","public_shares":{"enabled":true,"authority_staging_root":"/private/custom-stage"}}`,
		"public-links.json": `{"schema":"filees.public-links/v1","cache":{"enabled":true,"root":"/data/custom-cache","max_size":10737418240}}`,
	}
	for name, raw := range before {
		writeStorageConfig(t, r, name, raw)
	}
	got, err := r.planConfigMigrations(storageManifest())
	if err != nil || len(got) != 0 {
		t.Fatalf("custom roots migrated: %+v %v", got, err)
	}
	for name, want := range before {
		got, _ := os.ReadFile(filepath.Join(r.Config.SysconfDir, name))
		if string(got) != want {
			t.Fatal("custom config changed")
		}
	}
}

func TestPublicStorageMigrationBudgetsConfiguredAuthorityMaxSize(t *testing.T) {
	r, _ := testRunner(t)
	r.Config.PublicDownloadsDir = "/home/_filees"
	writeStorageConfig(t, r, "server.json", `{"schema":"filees.server-toolchain/v2","display_name":"Cloud","public_shares":{"enabled":true,"max_size":3221225472}}`)
	got, err := r.planConfigMigrations(storageManifest())
	if err != nil || len(got) != 1 || got[0].Directories[0].Required != 6<<30 {
		t.Fatalf("configured leaf budget ignored: %+v %v", got, err)
	}
}
func TestPublicStorageDirectoriesRetryRefuseWrongModeAndCapacity(t *testing.T) {
	r, root := testRunner(t)
	target := filepath.Join(root, "downloads")
	migration := ConfigMigration{Directories: []storageDirectory{
		{Root: target, Path: filepath.Join(target, "authority"), Owner: "_filees-state"},
		{Root: target, Path: filepath.Join(target, "cache"), Owner: "_filees-links"},
	}}
	for i := 0; i < 2; i++ {
		if err := r.prepareStorageDirectories([]ConfigMigration{migration}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(target, "cache"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := r.prepareStorageDirectories([]ConfigMigration{migration}); err == nil {
		t.Fatal("silently accepted/changed public cache")
	}
	if err := os.Chmod(filepath.Join(target, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	impossible := ConfigMigration{Directories: []storageDirectory{{Root: filepath.Join(root, "too-large"), Path: filepath.Join(root, "too-large", "cache"), Owner: "_filees-links", Required: math.MaxInt64}}}
	if err := r.prepareStorageDirectories([]ConfigMigration{impossible}); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("space check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "too-large")); !os.IsNotExist(err) {
		t.Fatal("failed capacity probe created directory")
	}
}
