//go:build openbsd

package servertool

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"filees/internal/mobileworker"
	"filees/internal/obsandbox"
	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type recoveryTestAuthority struct{ root string }

func (a recoveryTestAuthority) Resolve(context.Context, string, string) (mobileworker.View, error) {
	return mobileworker.View{RepoPath: filepath.Join(a.root, "repo"), Generation: 1, Access: "rw"}, nil
}
func (recoveryTestAuthority) List(context.Context, string) (mobileworker.Projection, error) {
	return mobileworker.Projection{}, nil
}

func TestMobileRecoveryWithProductionSandbox(t *testing.T) {
	if root := os.Getenv("FILEES_MOBILE_RECOVERY_TEST_ROOT"); root != "" {
		svn, _ := exec.LookPath("svn")
		look, _ := exec.LookPath("svnlook")
		ledger := filepath.Join(root, "ledger")
		for _, dir := range []string{ledger, filepath.Join(root, "service"), filepath.Join(root, "gui")} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		var body bytes.Buffer
		zw := zip.NewWriter(&body)
		_ = zw.SetComment(v1.TreePackComment)
		w, _ := zw.Create("proof.txt")
		_, _ = w.Write([]byte("sandbox recovery"))
		_ = zw.Close()
		sum := sha256.Sum256(body.Bytes())
		id := uuid.NewString()
		p := v1.UploadTreePayload{RepoID: "repo", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(body.Len()), Sha256: hex.EncodeToString(sum[:])}
		a := mobileworker.Appender{Authority: recoveryTestAuthority{root}, Reader: mobileworker.SVNReader{SvnPath: svn, SvnlookPath: look}, Committer: mobileworker.SVNAppender{SvnPath: svn, SvnlookPath: look}, Ledger: mobileworker.Ledger{Dir: ledger}}
		if err := os.Chdir(root); err != nil {
			t.Fatal(err)
		}
		profile := obsandbox.Profile{Name: "mobile-recovery-test", Promises: mobileEntryPromises, Paths: mobileUnveilPaths(root, filepath.Join(root, "service"), filepath.Join(root, "gui"), ledger, svn, look)}
		if err := obsandbox.Apply(profile); err != nil {
			t.Fatal(err)
		}
		first, err := a.UploadTree(context.Background(), "client", id, p, bytes.NewReader(body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		rec, err := a.Ledger.Lookup(id)
		if err != nil {
			t.Fatal(err)
		}
		rec.State = v1.OpStateCommitting
		rec.Revision = 0
		if err := a.Ledger.Put(*rec); err != nil {
			t.Fatal(err)
		}
		recovered, err := a.UploadTree(context.Background(), "client", id, p, bytes.NewReader(body.Bytes()))
		if err != nil || recovered.Revision != first.Revision {
			t.Fatalf("recovery %+v %v", recovered, err)
		}
		return
	}
	mobileRequireSVN(t)
	root := t.TempDir()
	newMobileSeededRepoAt(t, filepath.Join(root, "repo"))
	child := exec.Command(os.Args[0], "-test.run=^TestMobileRecoveryWithProductionSandbox$", "-test.v")
	child.Env = append(os.Environ(), "FILEES_MOBILE_RECOVERY_TEST_ROOT="+root)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("native sandbox: %v\n%s", err, out)
	}
}
