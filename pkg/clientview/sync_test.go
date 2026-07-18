package clientview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeUpdater struct {
	calls int
	err   error
}

func (f *fakeUpdater) Update(context.Context, string) (string, error) {
	f.calls++
	return "updated", f.err
}

func TestSyncPublishesOnlyValidatedNewerProjection(t *testing.T) {
	root := t.TempDir()
	wc := filepath.Join(root, "wc")
	cache := filepath.Join(root, "cache", "view.json")
	viewPath := filepath.Join(wc, "view.json")
	if err := os.MkdirAll(wc, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(view View) {
		raw, _ := json.Marshal(view)
		if err := os.WriteFile(viewPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := fixture()
	write(first)
	updater := &fakeUpdater{}
	if got, changed, err := Sync(t.Context(), updater, SyncConfig{WorkingCopy: wc, RelativeViewPath: "view.json", CachePath: cache}); err != nil || !changed || got.Generation != 1 {
		t.Fatalf("first changed=%v view=%+v err=%v", changed, got, err)
	}
	bad := first
	bad.Generation = 0
	write(bad)
	if _, _, err := Sync(t.Context(), updater, SyncConfig{WorkingCopy: wc, RelativeViewPath: "view.json", CachePath: cache}); err == nil {
		t.Fatal("invalid projection replaced cache")
	}
	if cached, err := Load(cache); err != nil || cached.Generation != 1 {
		t.Fatalf("cache=%+v err=%v", cached, err)
	}
}

func TestSyncTransportFailureDoesNotReadOrPublishProjection(t *testing.T) {
	root := t.TempDir()
	updater := &fakeUpdater{err: errors.New("offline")}
	_, _, err := Sync(t.Context(), updater, SyncConfig{WorkingCopy: filepath.Join(root, "wc"), RelativeViewPath: "view.json", CachePath: filepath.Join(root, "cache.json")})
	if err == nil || updater.calls != 1 {
		t.Fatalf("calls=%d err=%v", updater.calls, err)
	}
}
