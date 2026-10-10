//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

func TestAnchorRetainedMetadataCompatibilityRejectsUnsafeTransitions(t *testing.T) {
	a := batchState("drawing.dwg", true)
	b := a
	b.USN = "99"
	if !anchorRetainedMetadataCompatible(a, b, *a.Size) {
		t.Fatal("clean USN advance refused")
	}
	for _, variant := range []string{"identity", "size", "dirty", "partial", "remote", "invalid", "regressed", "ordinary"} {
		t.Run(variant, func(t *testing.T) {
			b := a
			switch variant {
			case "identity":
				b.Identity = "different"
			case "size":
				size := *a.Size + 1
				b.Size = &size
			case "dirty":
				b.InSync = false
			case "partial":
				b.Partial = true
			case "remote":
				b.Local = false
			case "invalid":
				b.USN = "unknown"
			case "regressed":
				b.USN = "0"
			case "ordinary":
				b.Placeholder = false
			}
			if anchorRetainedMetadataCompatible(a, b, *a.Size) {
				t.Fatal("unsafe transition accepted")
			}
		})
	}
}

// The metadata reader is injected; actual Windows guarded handles, retained
// bytes, private journal and cleanup are used, without a provider mutation.
func TestAnchorRetainedPreparationRefreshesUSNOrRetiresPreEffectCopy(t *testing.T) {
	for _, variant := range []string{"clean-advance", "dirty", "initial-dirty", "unstable"} {
		t.Run(variant, func(t *testing.T) {
			wc, store := t.TempDir(), t.TempDir()
			p := newLocalStateProbe(t, wc, "", "f\t8192\tremote.txt\tremote.txt\n")
			body := []byte("retained bytes")
			path := filepath.Join(wc, "drawing.dwg")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			identity := anchorReleaseIdentity{ServerID: "fixture", RepoID: "repo", RepoURL: "file:///fixture", RepoUUID: uuid.NewString(), WC: wc, Rel: "drawing.dwg", Revision: 1}
			p.accept(identity.Rel, true)
			m := &anchorManager{helper: p.helper}
			s, err := m.call(t.Context(), "", "file-state", "--root", wc, "--rel", identity.Rel)
			if err != nil {
				t.Fatal(err)
			}
			usn, err := strconv.ParseInt(s.USN, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			advanced := strconv.FormatInt(usn+1, 10)
			calls := 0
			j, err := prepareAnchorReleaseJournal(t.Context(), store, identity, func(context.Context) (cfapiAnswer, error) {
				calls++
				current := s
				if calls >= 3 {
					current.USN = advanced
				}
				if variant == "dirty" && calls >= 3 {
					current.InSync = false
				}
				if variant == "initial-dirty" {
					current.InSync = false
				}
				if variant == "unstable" && calls >= 4 {
					current.USN = strconv.FormatInt(usn+2, 10)
				}
				return current, nil
			})
			if variant == "clean-advance" {
				if err != nil {
					t.Fatal(err)
				}
				r, err := j.inspect(t.Context())
				if err != nil || r.USN != advanced || calls != 4 {
					t.Fatal(r, calls, err)
				}
			} else {
				if !errors.Is(err, errAnchorReleaseMetadataBusy) || j != nil {
					t.Fatal(j, err)
				}
				entries, err := os.ReadDir(store)
				if err != nil || len(entries) != 0 {
					t.Fatal("retry leaked retained copies", entries, err)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(body) {
				t.Fatal("source changed", err)
			}
		})
	}
}
