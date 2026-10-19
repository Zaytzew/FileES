//go:build !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"filees/pkg/localrepo"
)

type conversionSeedReader struct {
	client.Client
	revision            int64
	summary             client.HistoryTreeSummary
	body                string
	headReads, listings int
}

func (r *conversionSeedReader) Revision(context.Context, string) (int64, error) {
	r.headReads++
	return 0, errors.New("HEAD must not be read for bound conversion")
}
func (r *conversionSeedReader) HistoryListTree(_ context.Context, _ string, revision int64, out string) (client.HistoryTreeSummary, error) {
	r.listings++
	r.revision = revision
	return r.summary, os.WriteFile(out, []byte(r.body), 0600)
}

func TestConversionSeedUsesExplicitRevisionAndRejectsUnboundTree(t *testing.T) {
	for _, mode := range []string{"empty-r0", "empty-r7", "wrong-revision", "wrong-count", "nonempty-r0", "invalid-revision"} {
		t.Run(mode, func(t *testing.T) {
			point := t.TempDir()
			if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			rev := int64(7)
			if mode == "empty-r0" || mode == "nonempty-r0" {
				rev = 0
			}
			reader := &conversionSeedReader{summary: client.HistoryTreeSummary{Revision: rev}}
			switch mode {
			case "wrong-revision":
				reader.summary.Revision = 8
			case "wrong-count":
				reader.summary.Files = 1
			case "nonempty-r0":
				reader.summary.Files = 1
				reader.body = "{\"path\":\"file.txt\",\"kind\":\"file\",\"size\":12}\n"
			case "invalid-revision":
				rev = -1
			}
			m := &anchorManager{newSVN: func(string) (client.Client, error) { return reader, nil }}
			err := m.seedAt(t.Context(), localrepo.Record{ServerID: "fixture", RepoURL: "svn+ssh://fixture/repo", LocalPath: point}, rev)
			if reader.headReads != 0 {
				t.Fatal("bound seed polled HEAD")
			}
			if mode == "empty-r0" || mode == "empty-r7" {
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(anchorSeedPath(point))
				if err != nil {
					t.Fatal(err)
				}
				var seed anchorSeed
				if json.Unmarshal(raw, &seed) != nil || seed.Revision != rev || seed.Schema != anchorSeedSchema {
					t.Fatal("wrong seed receipt", string(raw))
				}
			} else {
				if err == nil {
					t.Fatal("unbound tree accepted")
				}
				if _, err := os.Stat(anchorSeedPath(point)); !os.IsNotExist(err) {
					t.Fatal("bad tree received success receipt", err)
				}
			}
		})
	}
}
