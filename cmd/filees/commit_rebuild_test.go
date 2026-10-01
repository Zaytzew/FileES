package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"filees/pkg/client"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
)

type canonicalSVNFake struct {
	client.Client
	client.TransactionCommitter
	repo                  config.Repo
	ownerErr, checkoutErr error
	head                  int64
	checkouts             int
	badUUID, dirty        bool
	afterCheckout         func()
}

func (c *canonicalSVNFake) GetInfo(_ context.Context, root string) (string, error) {
	if !canonicalExists(filepath.Join(root, ".svn")) {
		return "", os.ErrNotExist
	}
	uuid := "repository-uuid"
	if c.badUUID && root != c.repo.LocalPath {
		uuid = "foreign"
	}
	return "URL: " + c.repo.RepoURL + "\nRepository UUID: " + uuid + "\nDepth: infinity\n", nil
}
func (c *canonicalSVNFake) CommitHead(context.Context, string) (int64, error) { return c.head, nil }
func (c *canonicalSVNFake) Revision(_ context.Context, root string) (int64, error) {
	b, err := os.ReadFile(filepath.Join(root, ".svn", "revision"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(string(b), 10, 64)
}
func (c *canonicalSVNFake) InspectCommitWriter(_ context.Context, root string) (string, error) {
	if c.ownerErr != nil {
		return "", c.ownerErr
	}
	b, err := os.ReadFile(filepath.Join(root, ".svn", "filees-native-writer-v1"))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(b), err
}
func (c *canonicalSVNFake) Status(_ context.Context, root string, _ []string) ([]client.StatusEntry, error) {
	if c.dirty && root != c.repo.LocalPath {
		return []client.StatusEntry{{Path: "plan.dwg", Item: "modified"}}, nil
	}
	b, _ := os.ReadFile(filepath.Join(root, "plan.dwg"))
	if string(b) == "local" {
		return []client.StatusEntry{{Path: "plan.dwg", Item: "conflicted", Props: "none", Conflicted: true}}, nil
	}
	var entries []client.StatusEntry
	if canonicalExists(filepath.Join(root, ".filees")) {
		entries = append(entries, client.StatusEntry{Path: ".filees", Item: "unversioned"})
	}
	if canonicalExists(filepath.Join(root, "!kolizje")) {
		entries = append(entries, client.StatusEntry{Path: "!kolizje", Item: "unversioned"})
	}
	return entries, nil
}
func (c *canonicalSVNFake) Checkout(_ context.Context, _ string, root string) (string, error) {
	c.checkouts++
	if c.checkoutErr != nil {
		return "", c.checkoutErr
	}
	if err := os.MkdirAll(filepath.Join(root, ".svn"), 0700); err != nil {
		return "", err
	}
	for path, value := range map[string]string{"plan.dwg": "server", ".svn/revision": strconv.FormatInt(c.head, 10)} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(value), 0600); err != nil {
			return "", err
		}
	}
	if c.afterCheckout != nil {
		c.afterCheckout()
	}
	return "", nil
}

func canonicalFixture(t *testing.T) (config.Repo, *canonicalSVNFake, *canonicalRecoveryJob, *contract.CommitRecoveryPlan) {
	t.Helper()
	repo := config.Repo{ServerID: "office", ID: "repo", RepoURL: "file:///repo", LocalPath: filepath.Join(t.TempDir(), "wc"), Access: "rw"}
	for path, value := range map[string]string{"plan.dwg": "local", "plan.dwg.r139": "older server", "notes.txt": "unversioned work", ".svn/revision": "139", ".svn/filees-native-writer-v1": "older-writer", ".filees/commit_cache/transaction.json": "attempting-newer-id", ".filees/state/client.uuid": "original-client", ".filees/logs/errors.jsonl": "historical HOLD"} {
		abs := filepath.Join(repo.LocalPath, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureWorkingCopyIdentity(repo.LocalPath, expectedWorkingCopyIdentity(repo.ServerID, repo.ID, repo.RepoURL)); err != nil {
		t.Fatal(err)
	}
	c := &canonicalSVNFake{repo: repo, head: 264}
	j, plan, err := planCanonicalWorkingCopy(t.Context(), repo, c, "newer-intent", c.head)
	if err != nil {
		t.Fatal(err)
	}
	return repo, c, j, plan
}

func TestCanonicalRecoveryAdvancedHeadAndForeignWriterPreservesEverything(t *testing.T) {
	repo, c, j, plan := canonicalFixture(t)
	if plan.Choice != contract.CommitRecoveryRebuild || plan.HeadRevision != 264 || len(plan.Conflicts) != 1 || plan.Archive != j.archive() {
		t.Fatal(plan)
	}
	if canonicalExists(canonicalJobPath(repo.LocalPath)) || c.checkouts != 0 {
		t.Fatal("planning changed disk/network state")
	}
	before, _ := os.ReadFile(filepath.Join(repo.LocalPath, ".filees/commit_cache/transaction.json"))
	if err := acceptCanonicalWorkingCopy(t.Context(), repo, c, j, plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if err := recoverCanonicalWorkingCopy(t.Context(), repo, c); err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]string{"plan.dwg": "local", "plan.dwg.r139": "older server", "notes.txt": "unversioned work"} {
		b, err := os.ReadFile(filepath.Join(repo.LocalPath, filepath.FromSlash(plan.ConflictCopy), path))
		if err != nil || string(b) != expected {
			t.Fatal(path, string(b), err)
		}
	}
	for path, expected := range map[string]string{"plan.dwg": "server", ".filees/state/client.uuid": "original-client", ".filees/logs/errors.jsonl": "historical HOLD"} {
		b, err := os.ReadFile(filepath.Join(repo.LocalPath, filepath.FromSlash(path)))
		if err != nil || string(b) != expected {
			t.Fatal(path, string(b), err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(j.archive(), ".filees/commit_cache/transaction.json"))
	owner, _ := os.ReadFile(filepath.Join(j.archive(), ".svn/filees-native-writer-v1"))
	if string(before) != string(after) || string(owner) != "older-writer" || canonicalExists(filepath.Join(repo.LocalPath, ".filees/commit_cache/transaction.json")) {
		t.Fatal("uncertain intent/writer lost or replayed")
	}
	receipt, err := readCanonicalJob(repo)
	if err != nil || receipt.Stage != "done" {
		t.Fatal(receipt, err)
	}
	if err := recoverCanonicalWorkingCopy(t.Context(), repo, c); err != nil || c.checkouts != 1 {
		t.Fatal("completed rebuild replayed", err)
	}
}

func TestCanonicalRecoveryRefusalsLeaveOriginalAndProof(t *testing.T) {
	for _, mode := range []string{"changed", "active-writer", "checkout-failed", "foreign-uuid", "dirty-checkout", "edit-during-checkout", "head-during-checkout", "backup-obstructed", "sparse"} {
		t.Run(mode, func(t *testing.T) {
			repo, c, j, plan := canonicalFixture(t)
			if mode == "changed" {
				os.WriteFile(filepath.Join(repo.LocalPath, "notes.txt"), []byte("later edit"), 0600)
			}
			if mode == "active-writer" {
				c.ownerErr = errors.New("native writer active")
			}
			if mode == "sparse" {
				repo.Sparse = true
				c.repo = repo
				if _, _, err := planCanonicalWorkingCopy(t.Context(), repo, c, "newer", 264); err == nil {
					t.Fatal("sparse admitted")
				}
				return
			}
			err := acceptCanonicalWorkingCopy(t.Context(), repo, c, j, plan.PlanID)
			if mode == "changed" || mode == "active-writer" {
				if err == nil || canonicalExists(canonicalJobPath(repo.LocalPath)) {
					t.Fatal("stale/active plan accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "checkout-failed":
				c.checkoutErr = errors.New("network interrupted")
			case "foreign-uuid":
				c.badUUID = true
			case "dirty-checkout":
				c.dirty = true
			case "edit-during-checkout":
				c.afterCheckout = func() { os.WriteFile(filepath.Join(repo.LocalPath, "notes.txt"), []byte("new work"), 0600) }
			case "head-during-checkout":
				c.afterCheckout = func() { c.head++ }
			case "backup-obstructed":
				c.afterCheckout = func() { os.Mkdir(filepath.Join(j.fresh(), "!kolizje"), 0700) }
			}
			if err := recoverCanonicalWorkingCopy(t.Context(), repo, c); err == nil {
				t.Fatal("unsafe reconstruction accepted")
			}
			b, _ := os.ReadFile(filepath.Join(repo.LocalPath, "plan.dwg"))
			owner, _ := os.ReadFile(filepath.Join(repo.LocalPath, ".svn/filees-native-writer-v1"))
			if string(b) != "local" || string(owner) != "older-writer" || canonicalExists(j.archive()) {
				t.Fatal("original changed before verified replacement")
			}
		})
	}
}

func TestCanonicalRecoveryResumesEveryRenameWindow(t *testing.T) {
	for _, stage := range []string{"prepared", "disabled-before-receipt", "disabled", "archived-before-receipt", "archived", "installed-before-receipt", "installed"} {
		t.Run(stage, func(t *testing.T) {
			repo, c, j, plan := canonicalFixture(t)
			if err := acceptCanonicalWorkingCopy(t.Context(), repo, c, j, plan.PlanID); err != nil {
				t.Fatal(err)
			}
			if err := recoverCanonicalWorkingCopy(t.Context(), repo, c); err != nil {
				t.Fatal(err)
			}
			// Recreate a crash topology using the completed, verified candidate
			// and the preserved original; no new checkout is allowed on resume.
			if err := os.Rename(repo.LocalPath, j.fresh()); err != nil {
				t.Fatal(err)
			}
			j.Stage = "prepared"
			if stage == "prepared" || strings.HasPrefix(stage, "disabled") {
				if err := os.Rename(j.archive(), repo.LocalPath); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(stage, "disabled") {
				if err := os.Rename(filepath.Join(repo.LocalPath, ".filees"), filepath.Join(repo.LocalPath, j.disabledMetadata())); err != nil {
					t.Fatal(err)
				}
				if stage == "disabled" {
					j.Stage = "disabled"
				}
			}
			if stage == "archived" || stage == "installed-before-receipt" {
				j.Stage = "archived"
			}
			if strings.HasPrefix(stage, "installed") {
				if err := os.Rename(j.fresh(), repo.LocalPath); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "installed" {
				j.Stage = "installed"
			}
			if err := writeCanonicalJSON(canonicalJobPath(repo.LocalPath), j); err != nil {
				t.Fatal(err)
			}
			if err := recoverCanonicalWorkingCopy(t.Context(), repo, c); err != nil {
				t.Fatal(err)
			}
			got, _ := readCanonicalJob(repo)
			if got.Stage != "done" || c.checkouts != 1 || !canonicalExists(j.archive()) {
				t.Fatal("resume repeated checkout/lost original", got)
			}
		})
	}
}

func TestCanonicalRecoveryLeaseIsExclusiveAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.lock")
	if err := withCanonicalRecoveryLock(path, func() error {
		if err := withCanonicalRecoveryLock(path, func() error { t.Error("second executor entered"); return nil }); err == nil {
			t.Fatal("lease not exclusive")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := withCanonicalRecoveryLock(path, func() error { return nil }); err != nil {
		t.Fatal("lease not released", err)
	}
}
