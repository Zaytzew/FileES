//go:build !windows

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/internal/serverinstall/state"
	"filees/internal/storagewatch"
)

func archiveInstallerFixture(t *testing.T) (*Runner, archiveInstallPaths) {
	t.Helper()
	r, root := testRunner(t)
	var p archiveInstallPaths
	p.Activation.Root = filepath.Join(root, "activation")
	p.Activation.ServiceWC = filepath.Join(root, "service-wc")
	p.Repositories.Root = filepath.Join(root, "repositories")
	for _, dir := range []string{r.Config.SysconfDir, p.Activation.Root, filepath.Join(p.Activation.ServiceWC, "admin", "repositories"), p.Repositories.Root} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Config.SysconfDir, "server.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return r, p
}

func TestArchiveInstallRefusesEveryCanonicalState(t *testing.T) {
	for _, policy := range []string{`{"state":"preparing"}`, `{"state":"freezing"}`, `{"state":"archived"}`, `{"state":"releasing"}`, `{"state":"released"}`, `{"state":"future"}`, `false`} {
		t.Run(policy, func(t *testing.T) {
			r, p := archiveInstallerFixture(t)
			file := filepath.Join(p.Activation.ServiceWC, "admin", "repositories", "repo.json")
			if err := os.WriteFile(file, []byte(`{"archive_policy":`+policy+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			err := r.withArchiveInstallAdmission(t.Context(), func() error { called = true; return nil })
			if !errors.Is(err, ErrArchiveMaintenance) || called {
				t.Fatal("archive install admitted", err, called)
			}
		})
	}
}

func TestArchiveInstallRefusesFenceAndOrphanRuntime(t *testing.T) {
	for _, kind := range []string{"fence", "runtime", "corrupt-authority", "symlink-root", "symlink-authority", "large-authority"} {
		t.Run(kind, func(t *testing.T) {
			r, p := archiveInstallerFixture(t)
			switch kind {
			case "fence":
				if err := os.Mkdir(filepath.Join(p.Activation.Root, "archive-admission"), 0700); err != nil {
					t.Fatal(err)
				}
			case "runtime":
				if err := os.MkdirAll(filepath.Join(p.Repositories.Root, "repo", "hooks", ".filees-archive-protection"), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink-root":
				if err := os.Symlink(t.TempDir(), filepath.Join(p.Repositories.Root, "alias")); err != nil {
					t.Fatal(err)
				}
			case "symlink-authority":
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(p.Activation.ServiceWC, "admin", "repositories", "repo.json")); err != nil {
					t.Fatal(err)
				}
			case "large-authority":
				if err := os.WriteFile(filepath.Join(p.Activation.ServiceWC, "admin", "repositories", "repo.json"), make([]byte, (1<<20)+1), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(filepath.Join(p.Activation.ServiceWC, "admin", "repositories", "repo.json"), []byte(`{`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			if err := r.withArchiveInstallAdmission(t.Context(), func() error { called = true; return nil }); err == nil || called {
				t.Fatal("unsafe metadata admitted", err, called)
			}
		})
	}
}

func TestArchiveInstallActionsRefuseBeforeTransactionRecovery(t *testing.T) {
	actions := map[string]func(*Runner) error{
		"apply":          func(r *Runner) error { return r.Apply(t.Context(), Options{}) },
		"allow-rollback": func(r *Runner) error { return r.Apply(t.Context(), Options{AllowRollback: true}) },
		"dry-run":        func(r *Runner) error { return r.Apply(t.Context(), Options{DryRun: true}) },
		"check":          func(r *Runner) error { return r.Check(t.Context(), Options{}) },
		"adopt":          func(r *Runner) error { return r.Adopt(t.Context(), Options{}) },
		"rollback":       func(r *Runner) error { return r.Rollback() },
		"purge":          func(r *Runner) error { return r.Purge(PurgeOptions{Yes: true, WipeState: true}) },
	}
	for name, action := range actions {
		t.Run(name, func(t *testing.T) {
			r, p := archiveInstallerFixture(t)
			marker := filepath.Join(p.Activation.Root, "archive-admission")
			if err := os.Mkdir(marker, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(r.Config.SbinDir, "worker")
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("new image"), 0700); err != nil {
				t.Fatal(err)
			}
			entry := state.HistoryEntry{ReleaseID: "interrupted", BackupDir: r.Config.BackupDir, Files: []state.BackupFile{{Target: target}}}
			if err := state.SaveTransaction(r.Config.StateDir, &state.Transaction{Entry: entry}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(state.TransactionPath(r.Config.StateDir))
			if err != nil {
				t.Fatal(err)
			}
			if err := action(r); !errors.Is(err, ErrArchiveMaintenance) {
				t.Fatal("action did not refuse before recovery", err)
			}
			after, err := os.ReadFile(state.TransactionPath(r.Config.StateDir))
			if err != nil || string(after) != string(before) {
				t.Fatal("transaction changed", err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "new image" {
				t.Fatal("image restored/deleted", err)
			}
		})
	}
}

func TestArchiveInstallRechecksAfterStagingAndSerializesFreeze(t *testing.T) {
	r, p := archiveInstallerFixture(t)
	if err := r.withArchiveInstallAdmission(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	gate := storagewatch.Gate{Root: p.Activation.Root}
	if err := gate.WithExclusive(t.Context(), func() error { return os.Mkdir(filepath.Join(p.Activation.Root, "archive-admission"), 0700) }); err != nil {
		t.Fatal(err)
	}
	if _, err := r.installStaged(nil, &state.State{}, "new", nil); !errors.Is(err, ErrArchiveMaintenance) {
		t.Fatal("staging race admitted install", err)
	}
	if _, err := os.Stat(r.Config.BackupDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cutover wrote backups", err)
	}
}

func TestArchiveInstallHoldsSameWriterLease(t *testing.T) {
	r, p := archiveInstallerFixture(t)
	err := r.withArchiveInstallAdmission(t.Context(), func() error {
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
		defer cancel()
		called := false
		err := (storagewatch.Gate{Root: p.Activation.Root}).WithExclusive(ctx, func() error { called = true; return nil })
		if !errors.Is(err, context.DeadlineExceeded) || called {
			t.Fatal("freeze overlapped install", err, called)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.withArchiveInstallAdmission(ctx, func() error { t.Fatal("canceled installer ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestArchiveInstallOrdinaryMetadataAndSandbox(t *testing.T) {
	r, p := archiveInstallerFixture(t)
	if err := os.WriteFile(filepath.Join(p.Activation.ServiceWC, "admin", "repositories", "repo.json"), []byte(`{"archive_policy":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.withArchiveInstallAdmission(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	specs, err := r.archiveInstallUnveils()
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, s := range specs {
		if s.Path == filepath.Join(p.Activation.Root, ".capacity-writers.lock") {
			seen = s.Perms == "rw"
		}
	}
	if !seen {
		t.Fatal("cutover lease unavailable after unveil")
	}
}

func TestArchiveInstallNativeSandbox(t *testing.T) {
	if runtime.GOOS != "openbsd" {
		t.Skip("actual unveil/pledge requires OpenBSD")
	}
	if os.Getenv("FILEES_TEST_ARCHIVE_INSTALL_SANDBOX") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestArchiveInstallNativeSandbox$", "-test.v")
		cmd.Env = append(os.Environ(), "FILEES_TEST_ARCHIVE_INSTALL_SANDBOX=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("actual sandbox: %v %s", err, out)
		}
		return
	}
	r, _ := archiveInstallerFixture(t)
	r.Config.ConfigPath = filepath.Join(r.Config.SysconfDir, "server.json")
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Fatal(err)
	}
	r.Config.SVNPath = svn
	if err := r.withArchiveInstallAdmission(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	sandboxEnabled = func() bool { return true }
	specs, err := r.baseUnveils()
	if err != nil {
		t.Fatal(err)
	}
	// The real apply profile already includes the repository-root read/write
	// path; admission itself only reads its shallow metadata.
	var paths archiveInstallPaths
	if err := readArchiveInstallJSON(r.Config.ConfigPath, &paths); err != nil {
		t.Fatal(err)
	}
	specs = append(specs, unveilSpec{Label: "repositories", Path: paths.Repositories.Root, Perms: "rwc"})
	if err := r.applyUnveils(specs); err != nil {
		t.Fatal(err)
	}
	if err := r.withArchiveInstallAdmission(t.Context(), func() error { return nil }); err != nil {
		t.Fatal("lease unavailable after real unveil", err)
	}
	if err := r.reducePledge(); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveInstallAndRestoreCannotSwitchAuthorityRoots(t *testing.T) {
	r, _ := archiveInstallerFixture(t)
	config := filepath.Join(r.Config.SysconfDir, "server.json")
	candidate := filepath.Join(t.TempDir(), "server-before.json")
	var other archiveInstallPaths
	other.Activation.Root = t.TempDir()
	data, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.installStaged([]StagedFile{{Target: config, StagePath: candidate}}, &state.State{}, "new", nil); err == nil {
		t.Fatal("install switched admission roots")
	}
	if err := r.checkArchiveRestoreConfiguration(state.HistoryEntry{Files: []state.BackupFile{{Target: config, BackupPath: candidate, Existed: true}}}); err == nil {
		t.Fatal("restore switched admission roots")
	}
	after, err := os.ReadFile(config)
	if err != nil || string(after) != string(before) {
		t.Fatal("configuration changed", err)
	}
}
