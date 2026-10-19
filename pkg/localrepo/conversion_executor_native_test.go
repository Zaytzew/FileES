//go:build windows && native_svn_probe

package localrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/svnurl"
	"filees/pkg/client"
	"golang.org/x/sys/windows"
)

// This is a deliberately TEST-ONLY effects adapter. It performs real native
// SVN/NTFS operations, but its point is a sparse WC, NOT a registered CFAPI
// point. Retirement moves the old WC to isolated custody, never deletes it.
// Real provider/ownership guards and daemon integration remain separate gates.
type conversionNativeFixture struct {
	cli                        client.Client
	root, state, uri, sourceID string
	plan                       conversionNativePlan
	onCopy                     func(int)
	onRollback                 func(string)
}

type conversionNativePlan struct {
	RootID   string
	Revision int64
	Local    map[string]string
}

func nativeConversionID(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", errors.New("fixture refuses reparse")
	}
	return fmt.Sprintf("%d:%d", info.VolumeSerialNumber, uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow)), nil
}

func conversionNativeHash(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func conversionNativeWrite(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func (f *conversionNativeFixture) Acquire(context.Context, ConversionRecord) (func(), error) {
	return func() {}, nil
}
func (f *conversionNativeFixture) Revalidate(ctx context.Context, r ConversionRecord) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r.SourcePath != f.root || (r.Prepared && r.SourceFileID != f.sourceID) {
		return errors.New("fixture binding changed")
	}
	if r.Prepared {
		digest, err := conversionNativeHash(r.PlanPath)
		if err != nil {
			return err
		}
		if digest != r.PlanSHA256 {
			return errors.New("plan digest changed")
		}
		raw, err := os.ReadFile(r.PlanPath)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &f.plan); err != nil {
			return err
		}
		if f.plan.RootID != r.SourceFileID || f.plan.Revision != r.Revision || int64(len(f.plan.Local)) != r.LocalObjects {
			return errors.New("plan binding changed")
		}
	}
	return nil
}
func (f *conversionNativeFixture) Prepare(ctx context.Context, r ConversionRecord) (ConversionPreparation, error) {
	id, err := nativeConversionID(f.root)
	if err != nil {
		return ConversionPreparation{}, err
	}
	if id != f.sourceID {
		return ConversionPreparation{}, errors.New("wrong original root")
	}
	inv, err := f.cli.(client.ConversionInventoryReader).ConversionInventory(ctx, f.root)
	if err != nil {
		return ConversionPreparation{}, err
	}
	if err = inv.CheckFullClean(); err != nil {
		return ConversionPreparation{}, err
	}
	p := conversionNativePlan{RootID: id, Local: map[string]string{}}
	for _, entry := range inv.Entries {
		if entry.Path == "." {
			p.Revision = *entry.Revision
		}
		if !entry.Versioned && entry.Path != ".filees" {
			hash, err := conversionNativeHash(filepath.Join(f.root, filepath.FromSlash(entry.Path)))
			if err != nil {
				return ConversionPreparation{}, err
			}
			p.Local[entry.Path] = hash
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return ConversionPreparation{}, err
	}
	planPath := filepath.Join(f.state, "plan.json")
	if err = conversionNativeWrite(planPath, raw); errors.Is(err, os.ErrExist) {
		prior, readErr := os.ReadFile(planPath)
		if readErr != nil {
			return ConversionPreparation{}, readErr
		}
		if !bytes.Equal(prior, raw) {
			return ConversionPreparation{}, errors.New("unacknowledged plan differs")
		}
		err = nil
	}
	if err != nil {
		return ConversionPreparation{}, err
	}
	f.plan = p
	sum := sha256.Sum256(raw)
	return ConversionPreparation{id, p.Revision, planPath, hex.EncodeToString(sum[:]), int64(len(p.Local))}, nil
}
func (f *conversionNativeFixture) checkOld(ctx context.Context, path string, r ConversionRecord) error {
	id, err := nativeConversionID(path)
	if err != nil {
		return err
	}
	if id != r.SourceFileID {
		return errors.New("original FileID mismatch")
	}
	inv, err := f.cli.(client.ConversionInventoryReader).ConversionInventory(ctx, path)
	if err != nil {
		return err
	}
	if err = inv.CheckFullClean(); err != nil {
		return err
	}
	for _, entry := range inv.Entries {
		if !entry.Versioned && entry.Path != ".filees" {
			if _, ok := f.plan.Local[entry.Path]; !ok {
				return errors.New("late local object")
			}
		}
	}
	for rel, want := range f.plan.Local {
		got, err := conversionNativeHash(filepath.Join(path, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if got != want {
			return errors.New("local bytes changed")
		}
	}
	return nil
}
func (f *conversionNativeFixture) Retain(ctx context.Context, r ConversionRecord) error {
	_, sourceErr := os.Lstat(r.SourcePath)
	_, backupErr := os.Lstat(r.BackupPath)
	if errors.Is(sourceErr, os.ErrNotExist) && backupErr == nil {
		return f.checkOld(ctx, r.BackupPath, r)
	}
	if sourceErr != nil || !errors.Is(backupErr, os.ErrNotExist) {
		return errors.New("ambiguous retain paths")
	}
	if err := f.checkOld(ctx, r.SourcePath, r); err != nil {
		return err
	}
	return os.Rename(r.SourcePath, r.BackupPath) // isolated single-writer fixture only
}
func (f *conversionNativeFixture) Point(ctx context.Context, r ConversionRecord) (ConversionPoint, error) {
	ownerPath := filepath.Join(f.state, "point-owner")
	if _, err := os.Lstat(r.SourcePath); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(r.SourcePath, 0700); err != nil {
			return ConversionPoint{}, err
		}
		id, err := nativeConversionID(r.SourcePath)
		if err != nil {
			return ConversionPoint{}, err
		}
		if err := conversionNativeWrite(ownerPath, []byte(r.ID+"\n"+id)); err != nil {
			return ConversionPoint{}, err
		}
	} else if err != nil {
		return ConversionPoint{}, err
	}
	id, err := nativeConversionID(r.SourcePath)
	if err != nil {
		return ConversionPoint{}, err
	}
	owner, err := os.ReadFile(ownerPath)
	if err != nil || string(owner) != r.ID+"\n"+id {
		return ConversionPoint{}, errors.New("occupied point is not owned")
	}
	if _, err := os.Stat(filepath.Join(r.SourcePath, ".svn")); errors.Is(err, os.ErrNotExist) {
		if _, err := f.cli.(interface {
			CheckoutDepthEmpty(context.Context, string, string) (string, error)
		}).CheckoutDepthEmpty(ctx, f.uri, r.SourcePath); err != nil {
			return ConversionPoint{}, err
		}
	}
	if _, err := f.cli.GetInfo(ctx, r.SourcePath); err != nil {
		return ConversionPoint{}, err
	}
	return ConversionPoint{id, "TEST sparse WC; no CFAPI provider"}, nil
}
func (f *conversionNativeFixture) Preserve(ctx context.Context, r ConversionRecord) error {
	for n, rel := range []string{"first.bak", "second.bak"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(r.SourcePath, rel)
		data, err := os.ReadFile(filepath.Join(r.BackupPath, rel))
		if err != nil {
			return err
		}
		if err = conversionNativeWrite(target, data); errors.Is(err, os.ErrExist) {
			got, readErr := conversionNativeHash(target)
			if readErr != nil {
				return readErr
			}
			if got != f.plan.Local[rel] {
				return errors.New("local destination collision")
			}
			err = nil
		}
		if err != nil {
			return err
		}
		if f.onCopy != nil {
			f.onCopy(n + 1)
		}
	}
	return nil
}
func (f *conversionNativeFixture) Verify(ctx context.Context, r ConversionRecord) error {
	id, err := nativeConversionID(r.SourcePath)
	if err != nil {
		return err
	}
	if id != r.PointFileID {
		return errors.New("point FileID mismatch")
	}
	inv, err := f.cli.(client.ConversionInventoryReader).ConversionInventory(ctx, r.SourcePath)
	if err != nil {
		return err
	}
	for _, entry := range inv.Entries {
		if entry.Path == "." {
			if entry.Depth != "empty" || entry.Item != "normal" || entry.Props == "modified" || entry.Revision == nil || *entry.Revision != r.Revision {
				return errors.New("sparse baseline mismatch")
			}
			continue
		}
		if _, ok := f.plan.Local[entry.Path]; !ok {
			return errors.New("unaccounted new point data")
		}
	}
	for rel, want := range f.plan.Local {
		got, err := conversionNativeHash(filepath.Join(r.SourcePath, rel))
		if err != nil {
			return err
		}
		if got != want {
			return errors.New("preserved bytes mismatch")
		}
	}
	old := r.BackupPath
	if _, err := os.Stat(old); errors.Is(err, os.ErrNotExist) {
		old = filepath.Join(f.state, "retired-source")
	}
	return f.checkOld(ctx, old, r)
}
func (f *conversionNativeFixture) Cleanup(ctx context.Context, r ConversionRecord) error {
	retired := filepath.Join(f.state, "retired-source")
	if _, err := os.Lstat(retired); err == nil {
		if _, err := os.Lstat(r.BackupPath); !errors.Is(err, os.ErrNotExist) {
			return errors.New("duplicate retirement")
		}
		return f.checkOld(ctx, retired, r)
	}
	if err := f.checkOld(ctx, r.BackupPath, r); err != nil {
		return err
	}
	return os.Rename(r.BackupPath, retired)
}
func (f *conversionNativeFixture) Rollback(ctx context.Context, r ConversionRecord) (string, error) {
	if id, err := nativeConversionID(r.SourcePath); err == nil && id == r.SourceFileID {
		if _, err := os.Lstat(r.BackupPath); !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("duplicate original during rollback")
		}
		return id, f.checkOld(ctx, r.SourcePath, r)
	}
	if err := f.checkOld(ctx, r.BackupPath, r); err != nil {
		return "", err
	}
	if _, err := os.Lstat(r.SourcePath); err == nil {
		owner, err := os.ReadFile(filepath.Join(f.state, "point-owner"))
		if err != nil {
			return "", err
		}
		id, err := nativeConversionID(r.SourcePath)
		if err != nil {
			return "", err
		}
		if string(owner) != r.ID+"\n"+id {
			return "", errors.New("foreign occupied source")
		}
		quarantine := filepath.Join(f.state, "preserved-new-point")
		if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("occupied rollback custody")
		}
		if err := os.Rename(r.SourcePath, quarantine); err != nil {
			return "", err
		}
		if f.onRollback != nil {
			f.onRollback("point-preserved")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(r.BackupPath, r.SourcePath); err != nil {
		return "", err
	}
	if f.onRollback != nil {
		f.onRollback("original-restored")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return r.SourceFileID, f.checkOld(ctx, r.SourcePath, r)
}

func nativeExecutorFixture(t *testing.T) (*ConversionExecutor, *conversionNativeFixture) {
	t.Helper()
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "WC-Zażółć")
	tool := func(name string, args ...string) {
		t.Helper()
		p := os.Getenv("FILEES_PROBE_" + strings.ToUpper(name))
		if p == "" {
			p = name
		}
		p, err := exec.LookPath(p)
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(p, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture %s: %v %s", name, err, out)
		}
	}
	tool("svnadmin", "create", repo)
	uri := svnurl.File(repo)
	tool("svn", "checkout", "-q", uri, wc)
	for n := range 24 {
		if err := os.WriteFile(filepath.Join(wc, fmt.Sprintf("file%02d.txt", n)), []byte("published bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool("svn", "add", "--force", wc)
	tool("svn", "propset", "svn:ignore", "*.bak", wc)
	tool("svn", "commit", "-q", "-m", "conversion execution fixture", wc)
	for _, name := range []string{"first.bak", "second.bak"} {
		if err := os.WriteFile(filepath.Join(wc, name), []byte("keep "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(filepath.Join(root, "lifecycle"))
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.EnsureConfiguredAttached("lab", "repo", "svn+ssh://fixture/repo", "rw", wc, "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "private-state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := CreateConversionJournal(filepath.Join(state, "conversion.json"), r)
	if err != nil {
		t.Fatal(err)
	}
	id, err := nativeConversionID(wc)
	if err != nil {
		t.Fatal(err)
	}
	f := &conversionNativeFixture{cli: client.New(client.Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli")}), root: wc, state: state, uri: uri, sourceID: id}
	return &ConversionExecutor{Journal: j, Effects: f}, f
}

func TestNativeConversionExecutorFilesystemReplay(t *testing.T) {
	for _, boundary := range []string{"retaining/effect", "creating/effect", "preserving/effect", "cleanup/effect", "partial-preserve"} {
		t.Run(boundary, func(t *testing.T) {
			e, f := nativeExecutorFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			e.Progress = func(p ConversionProgress) {
				if string(p.Phase)+"/"+p.Boundary == boundary {
					cancel()
				}
			}
			if boundary == "partial-preserve" {
				f.onCopy = func(n int) {
					if n == 1 {
						cancel()
					}
				}
			}
			if _, err := e.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("interruption: %v", err)
			}
			reopenConversionExecutor(t, e)
			e.Progress = nil
			fresh := *f
			fresh.plan = conversionNativePlan{}
			fresh.onCopy = nil
			e.Effects = &fresh
			r, err := e.Run(t.Context())
			if err != nil || r.Phase != ConversionComplete {
				t.Fatalf("replay=%+v %v", r, err)
			}
			if _, err := os.Stat(filepath.Join(f.root, "file00.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("sparse point unexpectedly fetched contents")
			}
			data, err := os.ReadFile(filepath.Join(f.state, "retired-source", "file00.txt"))
			if err != nil || string(data) != "published bytes" {
				t.Fatal("lost retained source bytes", err)
			}
		})
	}
}

func TestNativeConversionExecutorLateWriteAndRollback(t *testing.T) {
	e, f := nativeExecutorFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	e.Progress = func(p ConversionProgress) {
		if p.Phase == ConversionCleanup && p.Boundary == "receipt" {
			cancel()
		}
	}
	if _, err := e.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r := e.Journal.Get()
	if err := os.WriteFile(filepath.Join(r.BackupPath, "file00.txt"), []byte("late edit"), 0600); err != nil {
		t.Fatal(err)
	}
	reopenConversionExecutor(t, e)
	e.Progress = nil
	if r, err := e.Run(t.Context()); err == nil || r.Blocked == "" {
		t.Fatal("late source write did not stop cleanup")
	}
	if data, err := os.ReadFile(filepath.Join(r.BackupPath, "file00.txt")); err != nil || string(data) != "late edit" {
		t.Fatal("late bytes lost")
	}
	if _, err := os.Stat(filepath.Join(f.state, "retired-source")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retired changed source")
	}

	e, f = nativeExecutorFixture(t)
	ctx, cancel = context.WithCancel(t.Context())
	e.Progress = func(p ConversionProgress) {
		if p.Phase == ConversionPreserving && p.Boundary == "receipt" {
			cancel()
		}
	}
	if _, err := e.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "new-user-data.txt"), []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	reopenConversionExecutor(t, e)
	e.Progress = nil
	r, err := e.Rollback(t.Context())
	if err != nil || r.Phase != ConversionRolledBack {
		t.Fatalf("rollback=%+v %v", r, err)
	}
	if id, err := nativeConversionID(f.root); err != nil || id != f.sourceID {
		t.Fatal("original root identity not restored")
	}
	data, err := os.ReadFile(filepath.Join(f.state, "preserved-new-point", "new-user-data.txt"))
	if err != nil || string(data) != "preserve me" {
		t.Fatal("new point data lost", err)
	}
}

func TestNativeConversionExecutorRollbackRestartsBetweenDirectoryMoves(t *testing.T) {
	for _, stop := range []string{"point-preserved", "original-restored"} {
		t.Run(stop, func(t *testing.T) {
			e, f := nativeExecutorFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			e.Progress = func(p ConversionProgress) {
				if p.Phase == ConversionPreserving && p.Boundary == "receipt" {
					cancel()
				}
			}
			if _, err := e.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.root, "new-user-data.txt"), []byte("keep after recovery crash"), 0600); err != nil {
				t.Fatal(err)
			}
			e.Progress = nil
			ctx, cancel = context.WithCancel(t.Context())
			f.onRollback = func(where string) {
				if where == stop {
					cancel()
				}
			}
			if r, err := e.Rollback(ctx); !errors.Is(err, context.Canceled) || r.Phase != ConversionRollingBack {
				t.Fatalf("rollback interrupted=%+v %v", r, err)
			}
			reopenConversionExecutor(t, e)
			fresh := *f
			fresh.plan = conversionNativePlan{}
			fresh.onRollback = nil
			e.Effects = &fresh
			r, err := e.Run(t.Context())
			if err != nil || r.Phase != ConversionRolledBack {
				t.Fatalf("rollback replay=%+v %v", r, err)
			}
			data, err := os.ReadFile(filepath.Join(f.state, "preserved-new-point", "new-user-data.txt"))
			if err != nil || string(data) != "keep after recovery crash" {
				t.Fatal("lost newer data", err)
			}
		})
	}
}

func TestNativeConversionExecutorRefusesAmbiguousCustody(t *testing.T) {
	for _, kind := range []string{"backup-collision", "unowned-point", "preserve-collision", "changed-plan", "missing-cleanup-proof"} {
		t.Run(kind, func(t *testing.T) {
			e, f := nativeExecutorFixture(t)
			stop := ConversionRetaining
			switch kind {
			case "unowned-point":
				stop = ConversionCreating
			case "preserve-collision":
				stop = ConversionPreserving
			case "missing-cleanup-proof":
				stop = ConversionCleanup
			}
			ctx, cancel := context.WithCancel(t.Context())
			e.Progress = func(p ConversionProgress) {
				if p.Phase == stop && p.Boundary == "receipt" {
					cancel()
				}
			}
			if _, err := e.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			r := e.Journal.Get()
			var proof string
			switch kind {
			case "backup-collision":
				if err := os.Mkdir(r.BackupPath, 0700); err != nil {
					t.Fatal(err)
				}
				proof = filepath.Join(r.BackupPath, "foreign.txt")
			case "unowned-point":
				if err := os.Mkdir(r.SourcePath, 0700); err != nil {
					t.Fatal(err)
				}
				proof = filepath.Join(r.SourcePath, "foreign.txt")
			case "preserve-collision":
				proof = filepath.Join(r.SourcePath, "first.bak")
			case "changed-plan":
				proof = r.PlanPath
			case "missing-cleanup-proof":
				if err := os.Rename(r.BackupPath, filepath.Join(f.state, "orphan-source")); err != nil {
					t.Fatal(err)
				}
			}
			if proof != "" {
				if err := os.WriteFile(proof, []byte("do not overwrite me"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			reopenConversionExecutor(t, e)
			e.Progress = nil
			fresh := *f
			fresh.plan = conversionNativePlan{}
			e.Effects = &fresh
			r, err := e.Run(t.Context())
			if err == nil || r.Blocked == "" || r.Phase != stop {
				t.Fatalf("ambiguous paths accepted: %+v %v", r, err)
			}
			if proof != "" {
				data, err := os.ReadFile(proof)
				if err != nil || string(data) != "do not overwrite me" {
					t.Fatal("collision overwritten", err)
				}
			}
		})
	}
}

func TestNativeConversionExecutorChildProcess(t *testing.T) {
	if os.Getenv("FILEES_CONVERSION_CRASH_CHILD") != "1" {
		return
	}
	j, err := OpenConversionJournal(os.Getenv("FILEES_CONVERSION_JOURNAL"))
	if err != nil {
		t.Fatal(err)
	}
	r := j.Get()
	f := &conversionNativeFixture{cli: client.New(client.Options{NativeSVNPath: os.Getenv("FILEES_SVN_PROBE"), SvnPath: filepath.Join(filepath.Dir(j.path), "absent-cli")}), root: r.SourcePath, state: filepath.Dir(j.path), uri: os.Getenv("FILEES_CONVERSION_FIXTURE_URI"), sourceID: os.Getenv("FILEES_CONVERSION_SOURCE_ID")}
	e := &ConversionExecutor{Journal: j, Effects: f, Progress: func(p ConversionProgress) {
		if string(p.Phase)+"/"+p.Boundary == os.Getenv("FILEES_CONVERSION_CRASH_AT") {
			os.Exit(73)
		}
	}}
	if _, err := e.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("child did not reach crash boundary")
}

func TestNativeConversionExecutorRecoversAfterProcessDeath(t *testing.T) {
	for _, boundary := range []string{"retaining/effect", "creating/effect", "cleanup/effect"} {
		t.Run(boundary, func(t *testing.T) {
			e, f := nativeExecutorFixture(t)
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestNativeConversionExecutorChildProcess$")
			cmd.Env = append(os.Environ(), "FILEES_CONVERSION_CRASH_CHILD=1", "FILEES_CONVERSION_JOURNAL="+e.Journal.path, "FILEES_CONVERSION_FIXTURE_URI="+f.uri, "FILEES_CONVERSION_SOURCE_ID="+f.sourceID, "FILEES_CONVERSION_CRASH_AT="+boundary)
			out, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 73 {
				t.Fatalf("child=%v %s", err, out)
			}
			reopenConversionExecutor(t, e)
			fresh := *f
			fresh.plan = conversionNativePlan{}
			e.Effects = &fresh
			r, err := e.Run(t.Context())
			if err != nil || r.Phase != ConversionComplete {
				t.Fatalf("post-death resume=%+v %v", r, err)
			}
		})
	}
}

// Keep a compile-time contract check on the real native-inspection adapter.
var _ ConversionEffects = (*conversionNativeFixture)(nil)
