package commit

import (
	"context"
	"errors"
	"filees/pkg/client"
	"os"
	"path/filepath"
	"testing"
)

type zeroMergeProbe struct {
	*mergeProbe
	client.HistoryTreeReader
	body      string
	listErr   error
	revisions []int64
}

func (p *zeroMergeProbe) HistoryListTree(_ context.Context, _ string, rev int64, out string) (client.HistoryTreeSummary, error) {
	p.revisions = append(p.revisions, rev)
	if p.listErr != nil {
		return client.HistoryTreeSummary{}, p.listErr
	}
	return client.HistoryTreeSummary{Revision: rev}, os.WriteFile(out, []byte(p.body), 0600)
}
func TestAnchorMergeExplicitZero(t *testing.T) {
	for _, mode := range []string{"empty", "nonempty", "offline", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			wc := t.TempDir()
			if err := os.WriteFile(filepath.Join(wc, "new.txt"), []byte("local"), 0600); err != nil {
				t.Fatal(err)
			}
			p := &zeroMergeProbe{mergeProbe: &mergeProbe{}}
			if mode == "nonempty" {
				p.body = "{\"path\":\"other.txt\",\"kind\":\"file\",\"size\":1}\n"
			}
			if mode == "offline" {
				p.listErr = errors.New("offline")
			}
			rev := int64(0)
			if mode == "unknown" {
				rev = -1
			}
			s := &Service{Anchor: true, Cli: p, RepoURL: "svn+ssh://host/repo", AnchorMergeRevision: func() (int64, error) { return rev, nil }}
			err := s.prepareAnchorMerge(t.Context(), wc, []string{"new.txt"})
			if (err == nil) != (mode == "empty") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if mode != "unknown" && (len(p.revisions) != 1 || p.revisions[0] != 0) {
				t.Fatal("not pinned to r0", p.revisions)
			}
			if len(p.adopted) > 0 {
				t.Fatal("r0 performed adoption", p.adopted)
			}
		})
	}
}
