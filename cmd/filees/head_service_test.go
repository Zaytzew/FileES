package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

type fakeHeadSVN struct{}

func (fakeHeadSVN) ListImmediate(context.Context, string) ([]client.RemoteEntry, error) {
	return nil, nil
}

func (fakeHeadSVN) CatURL(_ context.Context, _ string, dest string) error {
	return os.WriteFile(dest, []byte("preview"), 0o600)
}

// A preview must open read-only: edits to it would stay in a temp folder and
// never reach the server, so the copy itself refuses them.
func TestHeadPreviewIsReadOnly(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "Oferta.xlsx")
	if err := (fakeHeadSVN{}).CatURL(context.Background(), "", dest); err != nil {
		t.Fatal(err)
	}
	if err := markPreviewReadOnly(dest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o600) })
	if err := os.WriteFile(dest, []byte("edited"), 0o600); err == nil {
		t.Fatal("the preview copy accepted a write")
	}
}
