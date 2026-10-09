//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
)

type anchorDeleteNativeProbe struct {
	client.RemoteFileDeleter
	sends int
	lost  bool
}

func (p *anchorDeleteNativeProbe) RemoteFileDelete(ctx context.Context, in client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
	p.sends++
	r, err := p.RemoteFileDeleter.RemoteFileDelete(ctx, in)
	if err == nil && p.lost {
		return nil, errors.New("isolated lost native reply after remote effect")
	}
	return r, err
}

// Real native RA/SVN and OS handles with a controlled ordinary-file completion
// fixture. NOT reception of a connected CFAPI coordinator (its callback still
// refuses remote deletion). The separate native callback probe measures that OS.
func TestAnchorDeleteJournalNativeSVN(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "lost-reply"}[lost], func(t *testing.T) {
			s, cb, in, snapshot, _ := testDeleteJournal(t)
			root := t.TempDir()
			repo, seed := filepath.Join(root, "repo"), filepath.Join(root, "seed")
			admin, cli, native := probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_SVN_PROBE")
			probeRun(t, root, admin, "create", repo)
			if err := os.Mkdir(seed, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(seed, in.Source), []byte("seed"), 0600); err != nil {
				t.Fatal(err)
			}
			uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
			probeRun(t, root, cli, "import", "-q", "-m", "deletion seed", seed, uri)
			s.record.RepoURL = uri
			in.RootURL = uri
			in.BaseRevision = 1
			svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
			id, err := svn.(interface {
				HistoryRepositoryUUID(context.Context, string) (string, error)
			}).HistoryRepositoryUUID(t.Context(), uri)
			if err != nil {
				t.Fatal(err)
			}
			in.UUID = id
			cb.Identity, _ = anchorRemoteIdentity(in.Source, 1, 4)
			size := int64(4)
			inspect := func(context.Context) (cfapiAnswer, error) {
				return cfapiAnswer{OK: true, Placeholder: true, InSync: true, Identity: cb.Identity, Size: &size, USN: "100"}, nil
			}
			prepareDeleteJournal(t, s, cb, in, snapshot, inspect)
			completeDeleteJournal(t, s, cb, in)
			s.close()
			remote := &anchorDeleteNativeProbe{RemoteFileDeleter: svn.(client.RemoteFileDeleter), lost: lost}
			_, err = sendAnchorFileDelete(t.Context(), s.record, remote)
			if (err != nil) != lost {
				t.Fatal(err)
			}
			for range 2 {
				receipt, p, err := inspectAnchorFileDelete(t.Context(), s.record, remote)
				if err != nil || receipt == nil || receipt.Revision != 2 || receipt.RemoteFileDelete != in || p == nil || *p != *snapshot {
					t.Fatal(receipt, p, err)
				}
			}
			if _, err := sendAnchorFileDelete(t.Context(), s.record, remote); err == nil || remote.sends != 1 {
				t.Fatal("native deletion replayed", err)
			}
			if out, err := exec.Command(cli, "list", uri).Output(); err != nil || len(out) != 0 {
				t.Fatal("remote file survived", string(out), err)
			}
			// Exact historical bytes survive; no new revision was created by inspection.
			if out, err := exec.Command(cli, "cat", "-r", "1", uri+"/"+url.PathEscape(in.Source)+"@1").Output(); err != nil || string(out) != "seed" {
				t.Fatal("history changed", string(out), err)
			}
			if !commit.HasPendingRemoteFileDelete(s.record.LocalPath) {
				t.Fatal("remote receipt prematurely retired local fence")
			}
		})
	}
}
