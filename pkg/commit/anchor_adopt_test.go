package commit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

type anchorAdoptProbe struct {
	client.Client
	calls int
}

func (p *anchorAdoptProbe) UpdateAdoptPath(context.Context, string, string, int64) (string, error) {
	p.calls++
	return "", nil
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
