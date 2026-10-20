package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"filees/pkg/client"
)

type anchorAdoptProbe struct {
	client.Client
	calls     int
	rows      []client.StatusEntry
	statusErr error
}

func (p *anchorAdoptProbe) Status(_ context.Context, _ string, paths []string) ([]client.StatusEntry, error) {
	if p.statusErr != nil {
		return nil, p.statusErr
	}
	if p.rows != nil {
		return p.rows, nil
	}
	return []client.StatusEntry{{Path: paths[0], Item: "unversioned"}}, nil
}

func TestAdoptPathRefusesScheduledAndUnknownStatus(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"added", "deleted", "missing", "obstructed", "incomplete", "unknown"} {
		p := &anchorAdoptProbe{rows: []client.StatusEntry{{Path: "file", Item: item}}}
		s := &Service{Cli: p}
		s.BindWorkingCopy("fixture", root)
		if err := s.AdoptPath(t.Context(), root, "file", 17); err == nil || p.calls != 0 {
			t.Fatal(item, err, p.calls)
		}
	}
}

func (p *anchorAdoptProbe) UpdateAdoptPath(context.Context, string, string, int64) (string, error) {
	p.calls++
	p.rows = []client.StatusEntry{{Path: "file", Item: "normal"}}
	return "", nil
}

func TestAdoptPathConcurrentCompletionsUseOneUpdate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &anchorAdoptProbe{}
	s := &Service{Cli: p}
	s.BindWorkingCopy("fixture", root)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := s.AdoptPath(t.Context(), root, "file", 17); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if p.calls != 1 {
		t.Fatalf("native adopting updates=%d", p.calls)
	}
	for _, rows := range [][]client.StatusEntry{{}, {{Path: "other", Item: "unversioned"}}} {
		p.rows = rows
		if err := s.AdoptPath(t.Context(), root, "file", 17); err == nil || p.calls != 1 {
			t.Fatal("unknown status admitted", err, p.calls)
		}
	}
	p.statusErr = errors.New("native status failed")
	if err := s.AdoptPath(t.Context(), root, "file", 17); !errors.Is(err, p.statusErr) || p.calls != 1 {
		t.Fatal("status failure hidden", err)
	}
}

func TestAdoptPathRechecksLocalFileBeforeSVN(t *testing.T) {
	root := t.TempDir()
	p := &anchorAdoptProbe{}
	s := &Service{Cli: p}
	s.BindWorkingCopy("fixture", root)
	for _, rel := range []string{"missing", "../outside", ".svn/private"} {
		if err := s.AdoptPath(t.Context(), root, rel, 31); err == nil {
			t.Fatalf("accepted %q", rel)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptPath(t.Context(), root, "dir", 31); err == nil {
		t.Fatal("accepted directory")
	}
	if p.calls != 0 {
		t.Fatal("SVN saw an unsafe object")
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptPath(t.Context(), root, "file", 31); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatal("full file not adopted")
	}
}

func TestAdoptPathDoesNotRewindAlreadyTrackedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("keep local work"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"normal", "modified", "replaced", "conflicted"} {
		p := &anchorAdoptProbe{rows: []client.StatusEntry{{Path: "file", Item: item}}}
		s := &Service{Cli: p}
		s.BindWorkingCopy("fixture", root)
		if err := s.AdoptPath(t.Context(), root, "file", 17); err != nil {
			t.Fatal(item, err)
		}
		if p.calls != 0 {
			t.Fatalf("%s triggered adopting update and could rewind BASE", item)
		}
	}
}
