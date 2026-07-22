package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, data string) string {
	t.Helper()
	data = strings.ReplaceAll(data, `"repo_url":"svn://example/`, `"repo_url":"svn+ssh://_filees-client@example/`)
	data = fmt.Sprintf(`{"transport":{"identity_file":"/tmp/filees-id","known_hosts":"/tmp/filees-known-hosts"},"repositories":%s}`, data)
	return writeRawConfig(t, data)
}

func writeRawConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadBatchSafetySettings(t *testing.T) {
	data := `[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","max_batch_mib":256,"backlog_flush_mib":768,"shutdown_commit_timeout":"7m"}]`
	path := writeConfig(t, data)
	repos, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].MaxBatchMiB != 256 || repos[0].BacklogFlushMiB != 768 || repos[0].ShutdownCommitTimeout != 7*time.Minute {
		t.Fatalf("decoded settings = %#v", repos[0])
	}
}

func TestGlobalReadOnlyRoleDegradesRepositoryAccess(t *testing.T) {
	path := writeRawConfig(t, `{"server_id":"office","server_display_name":"filees.example.net","client_role":"ro","transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"repositories":[{"id":"r","repo_url":"svn+ssh://_filees-client@example/r","local_path":"/tmp/role-ro","commit_interval":"1m","access":"rw"}]}`)
	repos, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].Access != "r" || repos[0].ServerID != "office" || repos[0].ClientRole != "ro" {
		t.Fatalf("repo=%+v", repos[0])
	}
	view, err := LoadClientView(path)
	if err != nil {
		t.Fatal(err)
	}
	if view.DisplayName != "filees.example.net" || view.ClientRole != "ro" {
		t.Fatalf("view=%+v", view)
	}
}

func TestLoadAcceptsDedicatedDataAccount(t *testing.T) {
	path := writeRawConfig(t, `{"transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"repositories":[{"id":"r","repo_url":"svn+ssh://_filees-data@example/r","local_path":"/tmp/data-account","commit_interval":"1m"}]}`)
	repos, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := repos[0].RepoURL; got != "svn+ssh://_filees-data@example/r" {
		t.Fatalf("repo URL = %q", got)
	}
}

func TestLoadClientViewProjection(t *testing.T) {
	path := writeRawConfig(t, `{"server_id":"office","transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"projection":{"working_copy":"/tmp/service-wc","relative_view_path":"clients/client/view.json","cache_path":"/tmp/cache/view.json","interval":"15s"},"repositories":[]}`)
	view, err := LoadClientView(path)
	if err != nil {
		t.Fatal(err)
	}
	if view.Projection == nil || view.Projection.WorkingCopy != "/tmp/service-wc" || view.Projection.RelativeViewPath != "clients/client/view.json" || view.Projection.CachePath != "/tmp/cache/view.json" || view.Projection.Interval != 15*time.Second {
		t.Fatalf("projection=%+v", view.Projection)
	}
	if view.IdentityFile != "/tmp/id" || view.KnownHosts != "/tmp/known" {
		t.Fatalf("transport=%+v", view)
	}
	if !view.Configured {
		t.Fatal("projection-backed client view is not marked configured")
	}
}

func TestLoadClientViewTransportOnlyDoesNotCreateSyntheticServer(t *testing.T) {
	path := writeRawConfig(t, `{"transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"repositories":[]}`)
	view, err := LoadClientView(path)
	if err != nil {
		t.Fatal(err)
	}
	if view.Configured {
		t.Fatalf("transport-only view unexpectedly configured: %+v", view)
	}
}

func TestLoadClientViewStrictOptionalUpdateConfig(t *testing.T) {
	path := writeRawConfig(t, `{
  "transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},
  "update":{"enabled":true,"repo_url":"https://releases.example/FILESS-BIN/","channel":"stable","state_path":"/tmp/filees/update.json","stage_root":"/tmp/filees/stage"},
  "repositories":[]
}`)
	view, err := LoadClientView(path)
	if err != nil {
		t.Fatal(err)
	}
	if view.Update == nil || view.Update.RepoURL != "https://releases.example/FILESS-BIN" || view.Update.Channel != "stable" || view.Update.Component != "desktop" || view.Update.Platform == "" || view.Update.SVNProgram != "svn" {
		t.Fatalf("update config = %+v", view.Update)
	}
	disabled := writeRawConfig(t, `{"transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"update":{"enabled":false,"repo_url":"not-used","state_path":"relative","stage_root":"relative"},"repositories":[]}`)
	view, err = LoadClientView(disabled)
	if err != nil || view.Update != nil {
		t.Fatalf("disabled update = %+v, %v", view.Update, err)
	}
}

func TestLoadClientViewRejectsUnsafeUpdateConfigAndTrustOverrides(t *testing.T) {
	updates := []string{
		`{"enabled":true,"repo_url":"file:///tmp/releases","state_path":"/tmp/state","stage_root":"/tmp/stage"}`,
		`{"enabled":true,"repo_url":"https://user:secret@example/releases","state_path":"/tmp/state","stage_root":"/tmp/stage"}`,
		`{"enabled":true,"repo_url":"https://example/releases?revision=old","state_path":"/tmp/state","stage_root":"/tmp/stage"}`,
		`{"enabled":true,"repo_url":"https://example/releases","channel":"../old","state_path":"/tmp/state","stage_root":"/tmp/stage"}`,
		`{"enabled":true,"repo_url":"https://example/releases","component":"server","state_path":"/tmp/state","stage_root":"/tmp/stage"}`,
		`{"enabled":true,"repo_url":"https://example/releases","state_path":"relative","stage_root":"/tmp/stage"}`,
		`{"enabled":true,"repo_url":"https://example/releases","state_path":"/tmp/state","stage_root":"/tmp/stage","public_key":"attacker.pub"}`,
		`{"enabled":true,"repo_url":"https://example/releases","state_path":"/tmp/state","stage_root":"/tmp/stage","verify_signature":false}`,
		`{"enabled":true,"repo_url":"https://example/releases","state_path":"/tmp/state","stage_root":"/tmp/stage","signify_program":"attacker-signify"}`,
	}
	for _, update := range updates {
		path := writeRawConfig(t, `{"transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"update":`+update+`,"repositories":[]}`)
		if _, err := LoadClientView(path); err == nil {
			t.Fatalf("accepted unsafe update config: %s", update)
		}
	}
}

func TestLoadClientViewRejectsUnsafeProjection(t *testing.T) {
	for _, projection := range []string{
		`{"working_copy":"relative","relative_view_path":"view.json","cache_path":"/tmp/cache"}`,
		`{"working_copy":"/tmp/wc","relative_view_path":"../view.json","cache_path":"/tmp/cache"}`,
		`{"working_copy":"/tmp/wc","relative_view_path":"view.json","cache_path":"relative"}`,
		`{"working_copy":"/tmp/wc","relative_view_path":"view.json","cache_path":"/tmp/cache","interval":"0s"}`,
	} {
		path := writeRawConfig(t, `{"transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"projection":`+projection+`,"repositories":[]}`)
		if _, err := LoadClientView(path); err == nil {
			t.Fatalf("unsafe projection accepted: %s", projection)
		}
	}
}

func TestLoadEditPassportSettings(t *testing.T) {
	path := writeConfig(t, `[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","edit_passports":true,"edit_passport_ttl":"15m","edit_passport_heartbeat":"5m","edit_passport_max_session":"24h","edit_passport_close_grace":"5m"}]`)
	repos, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r := repos[0]
	if !r.EditPassports || r.EditPassportTTL != 15*time.Minute || r.EditPassportHeartbeat != 5*time.Minute || r.EditPassportMaxSession != 24*time.Hour || r.EditPassportCloseGrace != 5*time.Minute {
		t.Fatalf("settings = %#v", r)
	}
}

func TestLoadRejectsUnsafeEditPassportDurations(t *testing.T) {
	for _, data := range []string{
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","edit_passport_ttl":"15m","edit_passport_heartbeat":"15m"}]`,
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","edit_passport_ttl":"15m","edit_passport_max_session":"10m"}]`,
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","edit_passport_heartbeat":"20m"}]`,
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","edit_passport_ttl":"25h"}]`,
	} {
		if _, err := Load(writeConfig(t, data)); err == nil {
			t.Fatalf("accepted unsafe passport config: %s", data)
		}
	}
}

func TestLoadRejectsFlushWatermarkBelowBatchSize(t *testing.T) {
	data := `[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","max_batch_mib":512,"backlog_flush_mib":128}]`
	path := writeConfig(t, data)
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted backlog_flush_mib below max_batch_mib")
	}
}

func TestLoadRejectsDuplicateIDs(t *testing.T) {
	path := writeConfig(t, `[
		{"id":"same","repo_url":"svn://example/a","local_path":"/tmp/a","commit_interval":"1m"},
		{"id":"same","repo_url":"svn://example/b","local_path":"/tmp/b","commit_interval":"1m"}
	]`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplikat") {
		t.Fatalf("Load error = %v, want duplicate ID", err)
	}
}

func TestLoadRejectsRelativeLocalPath(t *testing.T) {
	path := writeConfig(t, `[{"id":"r","repo_url":"svn://example/r","local_path":"relative","commit_interval":"1m"}]`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "bezwzględna") {
		t.Fatalf("Load error = %v, want absolute path error", err)
	}
}

func TestLoadRejectsNestedRootsInBothOrders(t *testing.T) {
	for _, paths := range [][2]string{{"/tmp/root", "/tmp/root/child"}, {"/tmp/root/child", "/tmp/root"}} {
		data := fmt.Sprintf(`[
			{"id":"a","repo_url":"svn://example/a","local_path":%q,"commit_interval":"1m"},
			{"id":"b","repo_url":"svn://example/b","local_path":%q,"commit_interval":"1m"}
		]`, paths[0], paths[1])
		if _, err := Load(writeConfig(t, data)); err == nil || !strings.Contains(err.Error(), "nakładające się korzenie") {
			t.Fatalf("Load(%v) error = %v, want overlap", paths, err)
		}
	}
}

func TestLoadRejectsSameRootThroughSymlink(t *testing.T) {
	root := t.TempDir()
	realRoot := filepath.Join(root, "real")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`[
		{"id":"a","repo_url":"svn://example/a","local_path":%q,"commit_interval":"1m"},
		{"id":"b","repo_url":"svn://example/b","local_path":%q,"commit_interval":"1m"}
	]`, realRoot, alias)
	if _, err := Load(writeConfig(t, data)); err == nil || !strings.Contains(err.Error(), "nakładające się korzenie") {
		t.Fatalf("Load error = %v, want symlink overlap", err)
	}
}

func TestLoadRejectsInvalidRegexAndTiers(t *testing.T) {
	tests := []string{
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","shout_patterns":["["]}]`,
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","commit_tiers":[{"max_mb":10,"interval":"1m"},{"max_mb":5,"interval":"1m"}]}]`,
		`[{"id":"r","repo_url":"svn://example/r","local_path":"/tmp/r","commit_interval":"1m","commit_tiers":[{"max_mb":0,"interval":"1m"},{"max_mb":5,"interval":"1m"}]}]`,
	}
	for _, data := range tests {
		if _, err := Load(writeConfig(t, data)); err == nil {
			t.Fatalf("Load accepted invalid config: %s", data)
		}
	}
}

func TestLoadRejectsLegacySVNCredentials(t *testing.T) {
	data := `[{"id":"r","repo_url":"svn+ssh://_filees-client@example/r","local_path":"/tmp/r","commit_interval":"1m","username":"legacy","password":"secret"}]`
	if _, err := Load(writeConfig(t, data)); err == nil {
		t.Fatal("Load accepted legacy SVN credentials")
	}
}

func TestLoadRejectsTrailingJSON(t *testing.T) {
	valid := `{"transport":{"identity_file":"/tmp/id","known_hosts":"/tmp/known"},"repositories":[]}`
	if _, err := Load(writeRawConfig(t, valid+` {}`)); err == nil {
		t.Fatal("Load accepted a second JSON document")
	}
}

func TestLoadRequiresRestrictedSVNSSHTransport(t *testing.T) {
	for _, data := range []string{
		`[{"id":"r","repo_url":"svn://legacy/r","local_path":"/tmp/r","commit_interval":"1m"}]`,
		`[{"id":"r","repo_url":"svn+ssh://other@example/r","local_path":"/tmp/r","commit_interval":"1m"}]`,
		`[{"id":"r","repo_url":"svn+ssh://_filees-client@example:2222/r","local_path":"/tmp/r","commit_interval":"1m"}]`,
	} {
		if _, err := Load(writeConfig(t, data)); err == nil {
			t.Fatalf("Load accepted unsupported transport: %s", data)
		}
	}
	missingTransport := `{"repositories":[{"id":"r","repo_url":"svn+ssh://_filees-client@example/r","local_path":"/tmp/r","commit_interval":"1m"}]}`
	if _, err := Load(writeRawConfig(t, missingTransport)); err == nil {
		t.Fatal("Load accepted config without installation transport")
	}
}
