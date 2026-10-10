//go:build windows && !nocfapi

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/anchorqueue"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
)

func TestQueuedReleaseRejectsChangedAttachmentButWaitsForProviderStartup(t *testing.T) {
	for _, changed := range []string{"startup", "operation", "root", "url", "server", "repo"} {
		t.Run(changed, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "point")
			life, err := localrepo.Open(filepath.Join(dir, "lifecycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			r, err := life.BeginAnchorAttach("server", "repo", root, false)
			if err != nil {
				t.Fatal(err)
			}
			url := "svn+ssh://user@example.test/repo"
			if _, err := life.ApproveAttach(r.OperationID, "server", "repo", url, "rw"); err != nil {
				t.Fatal(err)
			}
			r, err = life.MarkAttached(r.OperationID, "repo")
			if err != nil {
				t.Fatal(err)
			}
			queue, err := anchorqueue.Open(filepath.Join(dir, "queue.json"))
			if err != nil {
				t.Fatal(err)
			}
			p := contract.RepoAnchorQueuePayload{IntentID: "a902d47b-46a6-4eb4-8229-a0120ab1de71", Files: 1,
				RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{Fingerprint: strings.Repeat("ab", 32),
					RepoAnchorPlanPayload: contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: ".", Action: "release"}}}
			op := r.OperationID
			switch changed {
			case "operation":
				op = "retired-attachment"
			case "root":
				root = filepath.Join(dir, "old-point")
			case "url":
				url += "-old"
			case "server":
				p.ServerID = "another-server"
			case "repo":
				p.RepoID = "another-repo"
			}
			x, err := queue.Enqueue(p, op, url, root)
			if err != nil {
				t.Fatal(err)
			}
			m := &anchorManager{lifecycle: life, releaseQueue: queue}
			ready, err := m.queuedReleaseReady(x)
			if ready || err != nil {
				t.Fatal(ready, err)
			}
			outcome := queue.List()[0].Result
			if changed == "startup" {
				if outcome.State != "queued" {
					t.Fatal(outcome)
				}
			} else if outcome.State != "failed" || outcome.FailureKey != "head.folder_changed" || outcome.Completed != 0 {
				t.Fatal("changed attachment admitted", outcome)
			}
		})
	}
}
