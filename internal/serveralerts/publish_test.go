package serveralerts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filees/pkg/alertchannel"
	"github.com/google/uuid"
)

func tool(t *testing.T, name string) string {
	t.Helper()
	p, e := exec.LookPath(name)
	if e != nil {
		t.Skip(name + " unavailable")
	}
	return p
}
func TestPublishMuccLifecycleAndConcurrentChanges(t *testing.T) {
	admin, look, mucc := tool(t, "svnadmin"), tool(t, "svnlook"), tool(t, "svnmucc")
	repo := filepath.Join(t.TempDir(), "service")
	if out, err := exec.Command(admin, "create", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	p := Publisher{Repository: repo, SVNLook: look, SVNMucc: mucc, TempDir: t.TempDir()}
	realm := uuid.NewString()
	ctx := context.Background()
	changed, err := p.Publish(ctx, realm, "disk.var", "error", "active", "Secret to intended realm")
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	changed, err = p.Publish(ctx, realm, "disk.var", "error", "active", "Secret to intended realm")
	if err != nil || changed {
		t.Fatal("not idempotent", err)
	}
	log, err := output(ctx, look, "log", repo)
	if err != nil || strings.Contains(string(log), "Secret") {
		t.Fatal("payload leaked through svn:log", string(log), err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_, e := p.Publish(ctx, realm, k, "warning", "active", "parallel")
			errs <- e
		}(key)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = p.Publish(ctx, realm, "disk.var", "error", "resolved", "Recovered")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := output(ctx, look, "cat", repo, "alerts/"+realm+"/snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := alertchannel.Decode(raw, realm)
	if err != nil || len(s.Incidents) != 3 {
		t.Fatal(s, err)
	}
	files, err := os.ReadDir(p.TempDir)
	if err != nil || len(files) != 0 {
		t.Fatal("staging leaked", files, err)
	}
}
