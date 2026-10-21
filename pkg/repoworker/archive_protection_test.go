package repoworker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func archiveProtectionFixture(t *testing.T) (string, string, ArchiveProtectionRecord) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), uuid.NewString())
	for _, dir := range []string{"db", "hooks/" + archiveGuardDirectory} {
		if err := os.MkdirAll(filepath.Join(repo, filepath.FromSlash(dir)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	r := ArchiveProtectionRecord{Schema: ArchiveGuardVersion, RepoID: filepath.Base(repo), RepositoryUUID: uuid.NewString(), OperationID: uuid.NewString(), Epoch: 1, State: "freezing", Revision: 7, At: time.Unix(1700000000, 0).UTC()}
	for name, data := range map[string]string{"format": "5\n", "db/uuid": r.RepositoryUUID + "\n"} {
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(name)), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(repo, "hooks", archiveGuardDirectory)
	if err := writeArchiveProtection(root, r); err != nil {
		t.Fatal(err)
	}
	return repo, root, r
}

func TestArchiveProtectionRequiredStateAndIdentityFailClosed(t *testing.T) {
	for _, name := range []string{"freezing", "archived", "missing", "corrupt", "trailing", "unknown field", "foreign UUID", "foreign repo", "oversize", "unknown state", "missing audit", "inconsistent audit"} {
		t.Run(name, func(t *testing.T) {
			repo, root, r := archiveProtectionFixture(t)
			path := filepath.Join(root, archiveGuardRecord)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "archived":
				r.State = "archived"
				err = writeArchiveProtection(root, r)
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0644)
			case "trailing":
				err = os.WriteFile(path, append(raw, []byte("{}")...), 0644)
			case "unknown field":
				err = os.WriteFile(path, bytes.Replace(raw, []byte("{\n"), []byte("{\n\"bypass\":true,"), 1), 0644)
			case "foreign UUID":
				err = os.WriteFile(filepath.Join(repo, "db", "uuid"), []byte(uuid.NewString()+"\n"), 0644)
			case "foreign repo":
				err = os.WriteFile(path, bytes.ReplaceAll(raw, []byte(r.RepoID), []byte(uuid.NewString())), 0644)
			case "oversize":
				err = os.WriteFile(path, bytes.Repeat([]byte(" "), 8193), 0644)
			case "unknown state":
				err = os.WriteFile(path, bytes.ReplaceAll(raw, []byte("freezing"), []byte("append-bypass")), 0644)
			case "missing audit":
				err = os.Remove(filepath.Join(root, "00000000000000000001-freezing.json"))
			case "inconsistent audit":
				err = os.WriteFile(filepath.Join(root, "00000000000000000001-freezing.json"), bytes.ReplaceAll(raw, []byte(r.OperationID), []byte(uuid.NewString())), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckArchiveWrite(repo); err == nil {
				t.Fatal("required archive state allowed a write")
			}
			var stderr bytes.Buffer
			if RunRealmQuotaGuard([]string{repo, "txn"}, &stderr) == 0 || stderr.Len() == 0 {
				t.Fatal("final commit guard did not refuse with a reason")
			}
		})
	}
	if err := CheckArchiveWrite(t.TempDir()); err != nil {
		t.Fatal("ordinary repository changed", err)
	}
	if err := CheckArchiveWrite("relative"); err == nil {
		t.Fatal("relative hook target accepted")
	}
}

func TestArchiveProtectionAuditAndGuardModes(t *testing.T) {
	repo, root, r := archiveProtectionFixture(t)
	if err := writeArchiveProtection(root, r); err != nil {
		t.Fatal("idempotent event", err)
	}
	r.Revision++
	if err := writeArchiveProtection(root, r); err == nil {
		t.Fatal("replaced an immutable audit event")
	}
	_, root2, r2 := archiveProtectionFixture(t)
	r2.State, r2.Epoch, r2.Reason = "released", 2, "authorized laboratory release"
	r2.PreviousSHA256 = strings.Repeat("1", 64)
	if err := writeArchiveProtection(root2, r2); err != nil {
		t.Fatal(err)
	}
	repo2 := filepath.Dir(filepath.Dir(root2))
	if err := CheckArchiveWrite(repo2); err != nil {
		t.Fatal("released guard", err)
	}
	var stderr bytes.Buffer
	if RunArchiveRevpropGuard([]string{repo, "1", "user", "svn:log", "M"}, &stderr) == 0 || !strings.Contains(stderr.String(), "administrative") {
		t.Fatal("revprops became mutable")
	}
	if _, err := os.Stat(filepath.Join(root, "00000000000000000001-freezing.json")); err != nil {
		t.Fatal(err)
	}
	if err := (ArchiveProtection{Repository: repo}).Freeze(context.Background(), r); err == nil {
		t.Fatal("freeze without session barrier accepted")
	}
	if !errors.Is(CheckArchiveWrite(repo), ErrArchiveReadOnly) {
		t.Fatal("refusal changed state")
	}
}

func TestArchiveProtectionBarrierCannotMaskOrSkipAction(t *testing.T) {
	want := errors.New("inspection failed")
	for _, mode := range []string{"skip", "swallow", "twice", "propagate"} {
		t.Run(mode, func(t *testing.T) {
			p := ArchiveProtection{WithSessionsStopped: func(ctx context.Context, fn func() error) error {
				switch mode {
				case "skip":
					return nil
				case "swallow":
					_ = fn()
					return nil
				case "twice":
					_ = fn()
					_ = fn()
					return nil
				default:
					return fn()
				}
			}}
			if err := p.withBarrier(t.Context(), func() error { return want }); err == nil {
				t.Fatal("invalid barrier returned success")
			}
		})
	}
}
