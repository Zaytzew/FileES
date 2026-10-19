//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

func TestAnchorDeleteResidentProofNeverTrustsCleanBitAlone(t *testing.T) {
	for _, scenario := range []string{"valid", "changed-bytes", "old-usn", "partial", "modified", "dirty"} {
		t.Run(scenario, func(t *testing.T) {
			s, cb, in, p, inspect := testDeleteJournal(t)
			state, _ := inspect(t.Context())
			state.Partial = false
			state.OnDisk = 4
			sum := sha1.Sum([]byte("seed"))
			s.contentSHA1 = hex.EncodeToString(sum[:])
			s.contentUSN = state.USN
			switch scenario {
			case "changed-bytes":
				if err := os.WriteFile(filepath.Join(s.record.LocalPath, in.Source), []byte("edit"), 0600); err != nil {
					t.Fatal(err)
				}
				h, _, err := pinMoveObject(s.record, in.Source, false)
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(h)
				if err := verifyAnchorDeleteContents(t.Context(), h, 4, s.contentSHA1); err == nil {
					t.Fatal("unpublished bytes accepted")
				}
				return
			case "old-usn":
				state.USN = "101"
			case "partial":
				state.Partial = true
				state.OnDisk = 3
			case "modified":
				state.Modified = 1
			case "dirty":
				state.InSync = false
			}
			err := s.prepare(t.Context(), cb, in, p, func(context.Context) (anchorDeleteFileState, error) { return state, nil })
			if (err == nil) != (scenario == "valid") {
				t.Fatal(scenario, err)
			}
		})
	}
}

type deletePreflightClient struct {
	client.Client
	history         []client.HistoryCommit
	historyErr      error
	observation     client.LockObservation
	reads, releases int
	releaseErr      error
	localBase       int64
	localErr        error
	metadata        func(string, client.RemoteFileDelete) (string, error)
}

func (c *deletePreflightClient) HistoryLog(context.Context, string, int64, int64, int) ([]client.HistoryCommit, error) {
	return c.history, c.historyErr
}
func (c *deletePreflightClient) ReadLockObservation(context.Context, string, string) (client.LockObservation, error) {
	c.reads++
	return c.observation, nil
}
func (c *deletePreflightClient) ReleaseLock(_ context.Context, _, _, token string) (*client.LockReleaseReceipt, string, error) {
	c.releases++
	if c.releaseErr != nil {
		return nil, "", c.releaseErr
	}
	if c.observation.Remote == nil || c.observation.Remote.Token != token {
		return &client.LockReleaseReceipt{Token: token, State: "not-held"}, "", nil
	}
	c.observation = client.LockObservation{}
	return &client.LockReleaseReceipt{Token: token, State: "released"}, "", nil
}
func (c *deletePreflightClient) Revision(context.Context, string) (int64, error) {
	return c.localBase, c.localErr
}
func (c *deletePreflightClient) LocalFileDeleteMetadata(_ context.Context, _, phase string, in client.RemoteFileDelete, _ int64) (string, error) {
	return c.metadata(phase, in)
}

func TestAnchorDeleteOlderIdentityNeedsCompleteUnchangedHistory(t *testing.T) {
	old, _ := anchorRemoteIdentity("PZT/fin.dwg", 39, 4)
	current, _ := anchorRemoteIdentity("PZT/fin.dwg", 41, 4)
	for _, scenario := range []string{"unchanged", "file-edit", "same-size-replace", "parent-copy", "root", "alias", "missing-revision", "duplicate-revision", "no-changes", "network", "size", "future"} {
		t.Run(scenario, func(t *testing.T) {
			c := &deletePreflightClient{history: []client.HistoryCommit{{Revision: 41, Changes: []client.HistoryChange{{Path: "elsewhere.txt", Action: "M"}}}, {Revision: 40, Changes: []client.HistoryChange{{Path: "other.txt", Action: "M"}}}}}
			tree := anchorRemoteTree{Revision: 41, Nodes: []anchorRemoteNode{{HistoryTreeNode: client.HistoryTreeNode{Path: "PZT/fin.dwg", Kind: "file", Size: 4}, Identity: current}}}
			switch scenario {
			case "file-edit":
				c.history[0].Changes[0].Path = "PZT/fin.dwg"
			case "same-size-replace":
				c.history[0].Changes[0] = client.HistoryChange{Path: "PZT/fin.dwg", Action: "R"}
			case "parent-copy":
				c.history[0].Changes[0] = client.HistoryChange{Path: "PZT", Action: "A", CopyFromRevision: 3}
			case "root":
				c.history[0].Changes[0].Path = ""
			case "alias":
				c.history[0].Changes[0].Path = "pzt/FIN.dwg"
			case "missing-revision":
				c.history = c.history[:1]
			case "duplicate-revision":
				c.history[1].Revision = 41
			case "no-changes":
				c.history[1].Changes = nil
			case "network":
				c.historyErr = errors.New("offline")
			case "size":
				tree.Nodes[0].Size++
			case "future":
				tree.Revision = 38
			}
			id, err := confirmAnchorDeleteSelection(t.Context(), c, localrepo.Record{RepoURL: "svn+ssh://host/repo"}, tree, old)
			if (err == nil) != (scenario == "unchanged") || (err == nil && id != current) {
				t.Fatalf("%s id=%q err=%v", scenario, id, err)
			}
		})
	}
}

func TestAnchorDeleteReleaseRequiresExactInactiveOwnToken(t *testing.T) {
	for _, scenario := range []string{"own", "foreign", "new-token", "no-local", "passport", "changes", "stale", "release-error"} {
		t.Run(scenario, func(t *testing.T) {
			c := &deletePreflightClient{observation: client.LockObservation{Local: &client.LockInfo{Token: "secret", Owner: "me"}, Remote: &client.LockInfo{Token: "secret", Owner: "me"}}}
			row := contract.Reservation{Path: "fin.dwg", Token: "secret", CanRelease: true}
			snap := ipcserver.ReservationSnapshot{}
			switch scenario {
			case "foreign":
				row.CanRelease = false
			case "new-token":
				c.observation.Remote.Token = "replacement"
			case "no-local":
				c.observation.Local = nil
			case "passport":
				row.ActivePassport = true
			case "changes":
				row.LocalChanges = true
			case "stale":
				snap.Stale = true
			case "release-error":
				c.releaseErr = errors.New("server refused unlock")
			}
			snap.Reservations = []contract.Reservation{row}
			s := ipcserver.New("unused")
			repo := s.RegisterRepoAccess("repo", "url", t.TempDir(), "server", "rw")
			repo.SetReservationListFunc(func(context.Context) (ipcserver.ReservationSnapshot, error) { return snap, nil })
			token := ""
			if c.observation.Local != nil {
				token = c.observation.Local.Token
			}
			err := releaseAnchorDeleteLock(t.Context(), repo, c, "wc", "fin.dwg", token)
			if (err == nil) != (scenario == "own") {
				t.Fatal(err)
			}
			if scenario != "own" && scenario != "release-error" && scenario != "new-token" && c.releases != 0 {
				t.Fatal("unsafe unlock")
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("token leaked")
			}
		})
	}
}

func TestAnchorDeleteDiagnosticIncludesPathCauseAndIgnoresShutdown(t *testing.T) {
	s, cb, _, _, _ := testDeleteJournal(t)
	dir := filepath.Join(s.record.LocalPath, ".filees", "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{}
	m.recordFileDeleteFailure(t.Context(), s.record, cb.Identity, errors.New("placeholder revision differs"))
	file := filepath.Join(dir, "errors.jsonl")
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), "FILE-3001") || !strings.Contains(string(raw), "placeholder revision differs") || !strings.Contains(string(raw), "dokument źródłowy.bin") {
		t.Fatal(string(raw), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.recordFileDeleteFailure(ctx, s.record, cb.Identity, context.Canceled)
	after, _ := os.ReadFile(file)
	if string(after) != string(raw) {
		t.Fatal("shutdown alarm")
	}
}
