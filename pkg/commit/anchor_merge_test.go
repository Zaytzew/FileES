package commit

import (
	"context"
	"errors"
	"filees/pkg/client"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type mergeProbe struct {
	client.Client
	status                       map[string]string
	children                     map[string][]client.HistoryEntry
	listErr, statusErr, adoptErr error
	revisions                    []int64
	adopted                      []string
}

func (p *mergeProbe) Status(_ context.Context, _ string, paths []string) ([]client.StatusEntry, error) {
	var out []client.StatusEntry
	for _, path := range paths {
		out = append(out, client.StatusEntry{Path: path, Item: p.status[path]})
	}
	return out, p.statusErr
}
func (p *mergeProbe) HistoryList(_ context.Context, target string, rev int64) ([]client.HistoryEntry, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	p.revisions = append(p.revisions, rev)
	return p.children[u.Path], p.listErr
}
func (p *mergeProbe) UpdateAdoptPathDepth(_ context.Context, _, rel string, rev int64, depth string) (string, error) {
	p.revisions = append(p.revisions, rev)
	p.adopted = append(p.adopted, rel+":"+depth)
	return "", p.adoptErr
}

func (p *mergeProbe) UnschedulePlainAdds(context.Context, string, []string) error {
	return errors.New("not expected in this probe")
}
func TestAnchorMergePinsBaseAndAdoptsOnlyExistingLocalSelection(t *testing.T) {
	wc := t.TempDir()
	os.Mkdir(filepath.Join(wc, "dir"), 0700)
	os.WriteFile(filepath.Join(wc, "dir/file"), []byte("local"), 0600)
	os.WriteFile(filepath.Join(wc, "dir/new"), []byte("new"), 0600)
	p := &mergeProbe{children: map[string][]client.HistoryEntry{"/repo": {{Name: "dir", Kind: "dir"}}, "/repo/dir": {{Name: "file", Kind: "file"}, {Name: "untouched", Kind: "file"}}}}
	s := &Service{Anchor: true, Cli: p, RepoURL: "svn+ssh://host/repo", AnchorMergeRevision: func() (int64, error) { return 7, nil }}
	if err := s.prepareAnchorMerge(t.Context(), wc, []string{"dir/new", "dir/file"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.adopted, []string{"dir:empty", "dir/file:infinity"}) {
		t.Fatal(p.adopted)
	}
	for _, rev := range p.revisions {
		if rev != 7 {
			t.Fatal("different baseline", rev)
		}
	}
	p.adopted = nil
	p.status = map[string]string{"dir": "normal", "dir/file": "modified", "dir/new": "unversioned"}
	if err := s.prepareAnchorMerge(t.Context(), wc, []string{"dir/new", "dir/file"}); err != nil || len(p.adopted) != 0 {
		t.Fatal("shrunk adopted directory", err, p.adopted)
	}
}
func TestAnchorMergeUnknownRemoteOrLocalStateCannotFallThroughToAdd(t *testing.T) {
	for _, mode := range []string{"list-error", "status-error", "missing-base", "kind-conflict", "adoption-error"} {
		t.Run(mode, func(t *testing.T) {
			wc := t.TempDir()
			os.WriteFile(filepath.Join(wc, "file"), []byte("keep"), 0600)
			p := &mergeProbe{children: map[string][]client.HistoryEntry{"/repo": {{Name: "file", Kind: "file"}}}}
			s := &Service{Anchor: true, Cli: p, RepoURL: "svn+ssh://host/repo", AnchorMergeRevision: func() (int64, error) { return 7, nil }}
			switch mode {
			case "list-error":
				p.listErr = errors.New("offline")
			case "status-error":
				p.statusErr = errors.New("cannot inspect")
			case "missing-base":
				s.AnchorMergeRevision = nil
			case "kind-conflict":
				p.children["/repo"][0].Kind = "dir"
			case "adoption-error":
				p.adoptErr = errors.New("busy")
			}
			if err := s.prepareAnchorMerge(t.Context(), wc, []string{"file"}); err == nil {
				t.Fatal("failed proof accepted")
			}
			if mode != "adoption-error" && len(p.adopted) != 0 {
				t.Fatal("mutation before complete proof", p.adopted)
			}
		})
	}
	s := &Service{}
	if err := s.prepareAnchorMerge(t.Context(), "unused", []string{"../invalid"}); err != nil {
		t.Fatal("ordinary WC behavior changed", err)
	}
}
