package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

type projectionSVN struct {
	client.Client
	checkouts, updates int
	url, wc            string
}

func (svn *projectionSVN) Checkout(_ context.Context, url, wc string) (string, error) {
	svn.checkouts++
	svn.url, svn.wc = url, wc
	return "checked out", nil
}

func (svn *projectionSVN) Update(_ context.Context, wc string) (string, error) {
	svn.updates++
	svn.wc = wc
	return "updated", nil
}

func TestServiceProjectionUpdaterChecksOutMissingWorkingCopyThenUpdates(t *testing.T) {
	wc := filepath.Join(t.TempDir(), "service-wc")
	svn := &projectionSVN{}
	updater := serviceProjectionUpdater{client: svn, url: "svn+ssh://_filees-client@example.net/"}
	if _, err := updater.Update(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if svn.checkouts != 1 || svn.updates != 0 || svn.url != updater.url || svn.wc != wc {
		t.Fatalf("checkout=%d update=%d url=%q wc=%q", svn.checkouts, svn.updates, svn.url, svn.wc)
	}
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := updater.Update(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if svn.checkouts != 1 || svn.updates != 1 {
		t.Fatalf("checkout=%d update=%d", svn.checkouts, svn.updates)
	}
}
