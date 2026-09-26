package main

import (
	"context"
	"encoding/json"
	"filees/internal/serveralerts"
	"filees/internal/svnurl"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/alertchannel"
	"filees/pkg/client"
	"github.com/google/uuid"
)

type alertReadStub struct {
	data      []byte
	size      int64
	revisions []int64
	fetches   int
}

func (r *alertReadStub) Revision(context.Context, string) (int64, error) { return 7, nil }
func (r *alertReadStub) HistoryList(_ context.Context, _ string, rev int64) ([]client.HistoryEntry, error) {
	r.revisions = append(r.revisions, rev)
	return []client.HistoryEntry{{Name: "snapshot.json", Kind: "file", Size: r.size}}, nil
}
func (r *alertReadStub) HistoryFetchFile(_ context.Context, _ string, rev int64, dest string) (int64, error) {
	r.fetches++
	r.revisions = append(r.revisions, rev)
	return int64(len(r.data)), os.WriteFile(dest, r.data, 0600)
}
func TestAlertReadPinnedAndBounded(t *testing.T) {
	realm := uuid.NewString()
	s, _, _ := (alertchannel.Snapshot{}).Change(realm, "disk", "warning", "active", "Low space", time.Now())
	raw, _ := json.Marshal(s)
	r := &alertReadStub{data: raw, size: int64(len(raw))}
	root := t.TempDir()
	got, err := readAlertSnapshot(context.Background(), r, "svn+ssh://host/alerts/"+realm, realm, root)
	if err != nil || got.Epoch != s.Epoch || r.revisions[0] != 7 || r.revisions[1] != 7 {
		t.Fatal(got, err)
	}
	files, _ := os.ReadDir(root)
	if len(files) != 0 {
		t.Fatal("left download artifacts")
	}
	r.size = alertchannel.MaxBytes + 1
	r.fetches = 0
	if _, err := readAlertSnapshot(context.Background(), r, "url", realm, root); err == nil || r.fetches != 0 {
		t.Fatal("oversize downloaded")
	}
}
func TestMailboxURLAndSourceIsolation(t *testing.T) {
	realm, id := uuid.NewString(), uuid.NewString()
	got, err := mailboxURL("svn+ssh://_filees-client@host/clients/"+id, id, realm)
	if err != nil || got != "svn+ssh://_filees-client@host/alerts/"+realm {
		t.Fatal(got, err)
	}
	for _, u := range []string{"https://evil/", "svn+ssh://host/clients/other", "svn+ssh://host/?x=y"} {
		if _, err := mailboxURL(u, id, realm); err == nil {
			t.Fatal(u)
		}
	}
	source := &serverAlertSources{boxes: map[string]*alertchannel.Inbox{}}
	for _, server := range []string{"a", "b"} {
		b, _ := alertchannel.Open(filepath.Join(t.TempDir(), "cache"), server, realm)
		s, _, _ := (alertchannel.Snapshot{}).Change(realm, "disk", "warning", "active", "Low", time.Now())
		_ = b.Accept(s, time.Now())
		source.boxes[server] = b
	}
	source.boxes["a"].Failed()
	rows, _ := source.Notices()
	for _, row := range rows {
		if row.Stale != (row.ServerID == "a") {
			t.Fatal(rows)
		}
	}
}

func TestAlertNativeReadWithoutWC(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Skip("FILEES_SVN_PROBE required for native integration")
	}
	svnadmin, e := exec.LookPath("svnadmin")
	if e != nil {
		t.Fatal(e)
	}
	look, e := exec.LookPath("svnlook")
	if e != nil {
		t.Fatal(e)
	}
	mucc, e := exec.LookPath("svnmucc")
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	out, e := exec.Command(svnadmin, "create", repo).CombinedOutput()
	if e != nil {
		t.Fatal(e, string(out))
	}
	realm := uuid.NewString()
	p := serveralerts.Publisher{Repository: repo, SVNLook: look, SVNMucc: mucc, TempDir: root}
	if _, e = p.Publish(t.Context(), realm, "storage.var", "error", "active", "Brak miejsca"); e != nil {
		t.Fatal(e)
	}
	native := client.New(client.Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "no-system-svn"), Timeout: 10 * time.Second})
	reader, ok := native.(alertReader)
	if !ok {
		t.Fatal("missing native reader")
	}
	snap, e := readAlertSnapshot(t.Context(), reader, svnurl.File(repo)+"/alerts/"+realm, realm, root)
	if e != nil {
		t.Fatal(e)
	}
	if len(snap.Incidents) != 1 || snap.Incidents[0].Text != "Brak miejsca" {
		t.Fatal(snap)
	}
	if e = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Name() == ".svn" {
			t.Fatal("reader created working copy")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
