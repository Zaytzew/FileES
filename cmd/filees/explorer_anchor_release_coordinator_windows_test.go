//go:build windows && !nocfapi

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

func TestAnchorReleaseProcessGuardAndSourceBinding(t *testing.T) {
	j, body := newTestAnchorReleaseJournal(t)
	r, err := j.inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := checkAnchorReleaseSource(t.Context(), r, false); err != nil {
		t.Fatal(err)
	}
	closeGuard, err := lockAnchorRelease(j)
	if err != nil {
		t.Fatal(err)
	}
	if closeSecond, err := lockAnchorRelease(j); err == nil {
		closeSecond()
		closeGuard()
		t.Fatal("second coordinator admitted")
	}
	closeGuard()
	path := filepath.Join(j.identity.WC, j.identity.Rel)
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkAnchorReleaseSource(t.Context(), r, false); err == nil {
		t.Fatal("same bytes replacement admitted")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, body) {
		t.Fatal("source mutated")
	}
	if got, err := os.ReadFile(filepath.Join(j.store, j.id, "data.bin")); err != nil || !bytes.Equal(got, body) {
		t.Fatal("copy mutated")
	}
}

func TestAnchorReleaseBaseUsesExactRecordedIdentity(t *testing.T) {
	j, _ := newTestAnchorReleaseJournal(t)
	r, err := j.inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"normal", "excluded", "busy", "uuid", "revision", "url", "path", "wc", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			got := testReleaseSnapshot(j)
			got.Excluded = false
			ctx := t.Context()
			switch name {
			case "excluded":
				got.Excluded = true
			case "busy":
				got.Idle = false
			case "uuid":
				got.RepositoryUUID = "other"
			case "revision":
				got.Revision++
			case "url":
				got.URL += "other"
			case "path":
				got.Path = "other"
			case "wc":
				got.WC += "other"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := checkAnchorReleaseBase(ctx, r, testReleaseExclusion(func(context.Context, string, string) (client.PathExclusion, error) { return got, nil }))
			if (err == nil) != (name == "normal") {
				t.Fatalf("base admitted: %v", err)
			}
		})
	}
}
