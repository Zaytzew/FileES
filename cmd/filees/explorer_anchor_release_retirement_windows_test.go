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

func TestAnchorRetirementMissingCopyFinishesOnlyVerifiedReceipt(t *testing.T) {
	for _, name := range []string{"after-delete", "no-intent", "changed-record", "replacement"} {
		t.Run(name, func(t *testing.T) {
			j, body := newTestAnchorReleaseJournal(t)
			requestTestReleaseExclusion(t, j)
			if err := j.request(t.Context(), "restore_requested"); err != nil {
				t.Fatal(err)
			}
			r, err := j.inspect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			copyPath := filepath.Join(j.store, j.id, "data.bin")
			f, info, err := openAnchorReleaseFile(copyPath, false)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			intent := anchorReleaseRetirement{Version: 1, Record: r, Copy: anchorReleaseID(info)}
			if name == "changed-record" {
				intent.Record.Identity.RepoID = "different"
			}
			if name != "no-intent" {
				if err := writeAnchorReleaseJSON(filepath.Join(j.store, j.id), 4, intent); err != nil {
					t.Fatal(err)
				}
			}
			// Simulate the missing-copy boundary without destroying fixture data.
			if err := os.Rename(copyPath, copyPath+".saved"); err != nil {
				t.Fatal(err)
			}
			if name == "replacement" {
				if err := os.WriteFile(copyPath, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ops := anchorReleaseRecoveryIO{svn: testReleaseExclusion(func(context.Context, string, string) (client.PathExclusion, error) {
				t.Fatal("receipt completion inspected/mutated WC")
				return client.PathExclusion{}, nil
			}), inspect: func(context.Context, string) (cfapiAnswer, error) {
				t.Fatal("receipt completion contacted provider")
				return cfapiAnswer{}, nil
			}}
			err = j.retireCopy(t.Context(), ops, nil)
			if (err == nil) != (name == "after-delete") {
				t.Fatalf("completion %s: %v", name, err)
			}
			if got, err := os.ReadFile(copyPath + ".saved"); err != nil || !bytes.Equal(got, body) {
				t.Fatal("saved fixture lost")
			}
			if name == "replacement" {
				if got, err := os.ReadFile(copyPath); err != nil || !bytes.Equal(got, body) {
					t.Fatal("replacement lost")
				}
			}
			_, receiptErr := os.Stat(filepath.Join(j.store, j.id, "05.json"))
			if (receiptErr == nil) != (name == "after-delete") {
				t.Fatal("false retirement receipt", receiptErr)
			}
		})
	}
}
