//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/localrepo"
)

func TestConversionContinuousCustodyReusesSourceDataProof(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file"), make([]byte, 8<<20), 0600); err != nil {
		t.Fatal(err)
	}
	pin, err := conversionPinTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeConversionEffects{old: pin, plan: conversionFSPlan{Objects: append([]conversionObject(nil), pin.Objects...)}}
	defer func() { f.old.close() }()
	file := pin.Files["sub/file"]
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if err = f.pinOld(t.Context(), localrepo.ConversionRecord{}, root, false); err != nil {
			t.Fatal(err)
		}
		if f.old != pin || f.old.Files["sub/file"] != file {
			t.Fatal("uninterrupted custody replaced source handles and reread data")
		}
		at, err := file.Seek(0, io.SeekCurrent)
		if err != nil || at != 0 {
			t.Fatal("source proof reread primary data", at, err)
		}
	}
}

func custodyFixture(t *testing.T) (*nativeConversionEffects, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	pin, err := conversionPinTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeConversionEffects{old: pin, plan: conversionFSPlan{Objects: append([]conversionObject(nil), pin.Objects...)}}
	t.Cleanup(func() { f.old.close() })
	return f, root
}

func TestConversionContinuousCustodyDeniesWritersAndRejectsNamespaceChanges(t *testing.T) {
	for _, action := range []string{"writer", "addition", "replacement", "ads", "metadata", "canceled", "closed-handle"} {
		t.Run(action, func(t *testing.T) {
			f, root := custodyFixture(t)
			path := filepath.Join(root, "file")
			ctx := t.Context()
			switch action {
			case "writer":
				writer, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err == nil {
					writer.Close()
					t.Fatal("data writer entered continuous custody")
				}
				if err = f.pinOld(ctx, localrepo.ConversionRecord{}, root, false); err != nil {
					t.Fatal(err)
				}
				return
			case "addition":
				if err := os.WriteFile(filepath.Join(root, "late"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				if err := os.Rename(path, filepath.Join(filepath.Dir(root), "preserved-original")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ads":
				if err := os.WriteFile(path+":late", []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				if err := os.Chmod(path, 0400); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0600) })
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed-handle":
				if err := f.old.Files["file"].Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.pinOld(ctx, localrepo.ConversionRecord{}, root, false); err == nil {
				t.Fatal("changed or lost custody accepted")
			}
		})
	}
}

func TestConversionLostCustodyRehashesAndRejectsSameMetadataEdit(t *testing.T) {
	f, root := custodyFixture(t)
	var bytes int64
	f.observeSourceRead = func(n int64) { bytes += n }
	f.old.close()
	f.old = nil
	if err := f.pinOld(t.Context(), localrepo.ConversionRecord{}, root, false); err != nil {
		t.Fatal(err)
	}
	if bytes != 5 {
		t.Fatal("lost custody reused a persistent hash", bytes)
	}
	path := filepath.Join(root, "file")
	prior, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	f.old.close()
	f.old = nil
	if err = os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, time.Now(), prior.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err = f.pinOld(t.Context(), localrepo.ConversionRecord{}, root, false); err == nil {
		t.Fatal("same-size same-mtime changed bytes accepted after custody loss")
	}
	if bytes != 10 {
		t.Fatal("changed source was not fully read", bytes)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "other" {
		t.Fatal("refusal changed source", err)
	}
}

func TestConversionCustodyCancellationDoesNotReadSource(t *testing.T) {
	f, root := custodyFixture(t)
	f.observeSourceRead = func(int64) { t.Fatal("canceled check read source data") }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.pinOld(ctx, localrepo.ConversionRecord{}, root, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConversionRetirementRechecksBytesAfterCustodyTransfer(t *testing.T) {
	f, root := custodyFixture(t)
	var want conversionObject
	for _, node := range f.plan.Objects {
		if node.Path == "file" {
			want = node
		}
	}
	path := filepath.Join(root, "file")
	prior, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	f.old.close()
	f.old = nil
	// Match the exact gap used by retirement: old pins closed, DELETE handle
	// not yet open. Equal size/mtime must not stand in for the final hash.
	if err = os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, time.Now(), prior.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err = conversionDelete(t.Context(), path, want); err == nil {
		t.Fatal("retirement accepted changed bytes after custody transfer")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "other" {
		t.Fatal("late data lost", err)
	}
}
