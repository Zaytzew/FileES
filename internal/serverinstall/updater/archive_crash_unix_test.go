//go:build !windows

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/serverinstall/config"
	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/platform"
	"filees/internal/serverinstall/state"
)

// Only an isolated fixture is passed across the process boundary. These tests
// do not enable positive archive upgrades: they exercise ordinary transaction
// recovery and its refusal after an archive sentinel appears.
type archiveCrashSpec struct {
	Config  config.Config
	Files   []StagedFile
	Orphans []manifest.Orphan
	Point   string
	Ready   string
	Recover bool
}

type archiveCrashOwnership struct {
	platform.SystemOwnership
	before int
	count  int
	pause  func()
}

func (o *archiveCrashOwnership) Apply(path string, ownership platform.Ownership) error {
	o.count++
	if o.count == o.before {
		o.pause()
	}
	return o.SystemOwnership.Apply(path, ownership)
}

type archiveCrashOutput struct {
	point string
	count int
	pause func()
}

func (o *archiveCrashOutput) Write(raw []byte) (int, error) {
	if strings.HasPrefix(string(raw), "INSTALL ") {
		o.count++
		if o.point == fmt.Sprintf("install-%d", o.count) {
			o.pause()
		}
	}
	if o.point == "orphan" && strings.HasPrefix(string(raw), "REMOVE-ORPHAN ") {
		o.pause()
	}
	return len(raw), nil
}

func TestArchiveInstallCrashChild(t *testing.T) {
	path := os.Getenv("FILEES_TEST_INSTALL_CRASH_SPEC")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var spec archiveCrashSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	pause := func() {
		if err := os.WriteFile(spec.Ready, []byte(spec.Point), 0600); err != nil {
			t.Fatal(err)
		}
		// The parent kills exactly this process handle. A timer keeps the
		// runtime alive without an artificial deadlock or premature os.Exit.
		<-time.After(time.Hour)
		t.Fatal("parent did not terminate the checkpoint process")
	}
	owner := &archiveCrashOwnership{pause: pause}
	if spec.Point == "before-publish" {
		owner.before = 1
	}
	if spec.Point == "recovery-partial" {
		owner.before = 2
	}
	r := &Runner{Config: &spec.Config, Ownership: owner, Out: &archiveCrashOutput{point: spec.Point, pause: pause}, In: strings.NewReader("")}
	if spec.Recover {
		if err := r.recoverInterrupted(); err != nil {
			t.Fatal(err)
		}
		return
	}
	st, err := state.Load(spec.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := r.installStaged(spec.Files, st, "crash-new", spec.Orphans)
	if err != nil {
		t.Fatal(err)
	}
	st.InstalledRelease, st.InstalledAt = entry.ReleaseID, entry.InstalledAt
	st.AdvanceFreshness(11, 2)
	st.History = append(st.History, entry)
	if err := state.Save(spec.Config.StateDir, st); err != nil {
		t.Fatal(err)
	}
	if spec.Point == "state-saved" {
		pause()
	}
	t.Fatal("fixture did not reach its requested checkpoint")
}

func archiveCrashFixture(t *testing.T) (*Runner, archiveInstallPaths, archiveCrashSpec) {
	t.Helper()
	r, p := archiveInstallerFixture(t)
	r.Config.OrphanFiles = "remove"
	r.Ownership = platform.SystemOwnership{}
	spec := archiveCrashSpec{Config: *r.Config, Ready: filepath.Join(r.Config.StateDir, "checkpoint")}
	identity := platform.Ownership{UID: os.Geteuid(), GID: os.Getegid()}
	for _, name := range []string{"worker", "authority-added", "entry"} {
		target, stage := filepath.Join(r.Config.SbinDir, name), filepath.Join(r.Config.StageDir, name)
		for _, dir := range []string{filepath.Dir(target), filepath.Dir(stage)} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(stage, []byte("new-"+name), 0500); err != nil {
			t.Fatal(err)
		}
		if name != "authority-added" {
			if err := os.WriteFile(target, []byte("old-"+name), 0540); err != nil {
				t.Fatal(err)
			}
		}
		spec.Files = append(spec.Files, StagedFile{Target: target, StagePath: stage, Mode: 0500, Ownership: identity})
	}
	orphan := filepath.Join(r.Config.SbinDir, "legacy")
	if err := os.WriteFile(orphan, []byte("old-orphan"), 0440); err != nil {
		t.Fatal(err)
	}
	spec.Orphans = []manifest.Orphan{{Target: orphan}}
	if err := state.Save(r.Config.StateDir, &state.State{InstalledRelease: "crash-old", HighestSequence: 10, SecurityEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	return r, p, spec
}

func archiveCrashProcess(t *testing.T, spec archiveCrashSpec, kill bool) (string, error) {
	t.Helper()
	if err := os.Remove(spec.Ready); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	file := filepath.Join(spec.Config.StateDir, "child-spec.json")
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveInstallCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "FILEES_TEST_INSTALL_CRASH_SPEC="+file)
	if !kill {
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if raw, err := os.ReadFile(spec.Ready); err == nil && string(raw) == spec.Point {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("checkpoint %s not reached: %v %s", spec.Point, err, output.String())
		case <-ctx.Done():
			cmd.Process.Kill()
			<-done
			t.Fatalf("checkpoint %s timed out: %s", spec.Point, output.String())
		case <-tick.C:
		}
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = <-done
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != -1 {
		t.Fatalf("child was not killed: %v %s", err, output.String())
	}
	return output.String(), nil
}

func archiveCrashAssertRestored(t *testing.T, r *Runner, spec archiveCrashSpec) {
	t.Helper()
	for _, file := range spec.Files {
		if filepath.Base(file.Target) == "authority-added" {
			if _, err := os.Lstat(file.Target); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("new target survived rollback", err)
			}
			continue
		}
		raw, err := os.ReadFile(file.Target)
		if err != nil || string(raw) != "old-"+filepath.Base(file.Target) {
			t.Fatal("old target not restored", file.Target, err)
		}
		if err := verifyInstalledMode(file.Target, 0540); err != nil {
			t.Fatal(err)
		}
		owner, err := r.Ownership.Stat(file.Target)
		if err != nil || owner != file.Ownership {
			t.Fatal("original ownership not restored", owner, err)
		}
	}
	raw, err := os.ReadFile(spec.Orphans[0].Target)
	if err != nil || string(raw) != "old-orphan" {
		t.Fatal("orphan not restored", err)
	}
	if err := verifyInstalledMode(spec.Orphans[0].Target, 0440); err != nil {
		t.Fatal(err)
	}
	owner, err := r.Ownership.Stat(spec.Orphans[0].Target)
	if err != nil || owner != spec.Files[0].Ownership {
		t.Fatal("orphan ownership not restored", owner, err)
	}
	if txn, err := state.LoadTransaction(r.Config.StateDir); err != nil || txn != nil {
		t.Fatal("journal not removed", err)
	}
	st, err := state.Load(r.Config.StateDir)
	if err != nil || st.InstalledRelease != "crash-old" || st.HighestSequence != 10 || st.SecurityEpoch != 1 {
		t.Fatal("failed upgrade changed release floor", st, err)
	}
}

func TestArchiveInstallKilledProcessRecovery(t *testing.T) {
	for _, point := range []string{"before-publish", "install-1", "install-2", "install-3", "orphan", "state-saved"} {
		t.Run(point, func(t *testing.T) {
			r, _, spec := archiveCrashFixture(t)
			spec.Point = point
			archiveCrashProcess(t, spec, true)
			txn, err := state.LoadTransaction(r.Config.StateDir)
			if err != nil || txn == nil || len(txn.Entry.Files) != 4 {
				t.Fatal("complete write-ahead journal absent", txn, err)
			}
			if err := validateEntryBackups(txn.Entry); err != nil {
				t.Fatal(err)
			}
			if point == "install-1" {
				raw, err := os.ReadFile(spec.Files[0].Target)
				if err != nil || string(raw) != "new-worker" {
					t.Fatal("partial publish was not real", err)
				}
				if _, err := os.Stat(spec.Files[1].Target); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("child ran beyond requested checkpoint", err)
				}
			}
			spec.Recover, spec.Point = true, ""
			if out, err := archiveCrashProcess(t, spec, false); err != nil {
				t.Fatal("fresh process recovery", err, out)
			}
			if out, err := archiveCrashProcess(t, spec, false); err != nil {
				t.Fatal("idempotent recovery", err, out)
			}
			if point != "state-saved" {
				archiveCrashAssertRestored(t, r, spec)
				return
			}
			for _, file := range spec.Files {
				raw, err := os.ReadFile(file.Target)
				if err != nil || string(raw) != "new-"+filepath.Base(file.Target) {
					t.Fatal("committed image rolled back", err)
				}
				if err := verifyInstalledMode(file.Target, 0500); err != nil {
					t.Fatal(err)
				}
				owner, err := r.Ownership.Stat(file.Target)
				if err != nil || owner != file.Ownership {
					t.Fatal("committed ownership changed", owner, err)
				}
			}
			if _, err := os.Stat(spec.Orphans[0].Target); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("committed orphan restored", err)
			}
			if txn, err := state.LoadTransaction(r.Config.StateDir); err != nil || txn != nil {
				t.Fatal("committed journal retained", err)
			}
			st, err := state.Load(r.Config.StateDir)
			if err != nil || st.InstalledRelease != "crash-new" || st.HighestSequence != 11 || st.SecurityEpoch != 2 || len(st.History) != 1 {
				t.Fatal("committed state changed", st, err)
			}
		})
	}
}

func TestArchiveInstallRecoveryCanItselfBeKilled(t *testing.T) {
	r, _, spec := archiveCrashFixture(t)
	spec.Point = "orphan"
	archiveCrashProcess(t, spec, true)
	journal, err := os.ReadFile(state.TransactionPath(r.Config.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	spec.Recover, spec.Point = true, "recovery-partial"
	archiveCrashProcess(t, spec, true)
	raw, err := os.ReadFile(spec.Orphans[0].Target)
	if err != nil || string(raw) != "old-orphan" {
		t.Fatal("first restore not published", err)
	}
	after, err := os.ReadFile(state.TransactionPath(r.Config.StateDir))
	if err != nil || !bytes.Equal(after, journal) {
		t.Fatal("unfinished recovery changed journal", err)
	}
	spec.Point = ""
	if out, err := archiveCrashProcess(t, spec, false); err != nil {
		t.Fatal(err, out)
	}
	archiveCrashAssertRestored(t, r, spec)
}

func TestArchiveInstallKilledProcessRefusesUnsafeRecovery(t *testing.T) {
	for _, cause := range []string{"archive", "corrupt-backup"} {
		t.Run(cause, func(t *testing.T) {
			r, p, spec := archiveCrashFixture(t)
			spec.Point = "install-2"
			archiveCrashProcess(t, spec, true)
			if cause == "archive" {
				if err := os.Mkdir(filepath.Join(p.Activation.Root, "archive-admission"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				txn, err := state.LoadTransaction(r.Config.StateDir)
				if err != nil || txn == nil {
					t.Fatal(err)
				}
				backup := txn.Entry.Files[0].BackupPath
				if err := os.Chmod(backup, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(backup, []byte("damaged"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{state.TransactionPath(r.Config.StateDir), state.Path(r.Config.StateDir), spec.Orphans[0].Target}
			for _, file := range spec.Files {
				paths = append(paths, file.Target)
			}
			txn, err := state.LoadTransaction(r.Config.StateDir)
			if err != nil || txn == nil {
				t.Fatal(err)
			}
			for _, file := range txn.Entry.Files {
				if file.Existed {
					paths = append(paths, file.BackupPath)
				}
			}
			before := make(map[string][]byte)
			infos := make(map[string]os.FileInfo)
			owners := make(map[string]platform.Ownership)
			for _, path := range paths {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = raw
				infos[path], err = os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				owners[path], err = r.Ownership.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			spec.Recover, spec.Point = true, ""
			out, err := archiveCrashProcess(t, spec, false)
			want := "sha256 mismatch"
			if cause == "archive" {
				want = ErrArchiveMaintenance.Error()
			}
			if err == nil || !strings.Contains(out, want) {
				t.Fatal("unsafe recovery not refused", err, out)
			}
			for _, path := range paths {
				raw, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(raw, before[path]) {
					t.Fatal("refused recovery changed target/journal/state", path, err)
				}
				info, err := os.Lstat(path)
				if err != nil || !os.SameFile(infos[path], info) || infos[path].Mode() != info.Mode() {
					t.Fatal("refused recovery changed inode/mode", path, err)
				}
				owner, err := r.Ownership.Stat(path)
				if err != nil || owners[path] != owner {
					t.Fatal("refused recovery changed ownership", path, err)
				}
			}
		})
	}
}

var _ io.Writer = (*archiveCrashOutput)(nil)
