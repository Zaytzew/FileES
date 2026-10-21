package repoworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"github.com/google/uuid"
)

func archiveReleasePolicyFixture(repo string) RepositoryArchivePolicy {
	old := testArchivePolicy()
	old.State = "archived"
	before := ArchiveProtectionRecord{Schema: ArchiveGuardVersion, RepoID: repo, OperationID: old.OperationID, RepositoryUUID: old.RepositoryUUID, Epoch: old.Epoch, Revision: old.Revision, State: "archived", At: old.StartedAt}
	audit := ArchiveReleaseAudit{Schema: archiveReleaseAuditSchema, RepoID: repo, OperationID: uuid.NewString(), Actor: "test-administrator", Reason: "isolated administrative test", RequestedAt: time.Now().UTC(), Previous: old, PreviousSHA256: archivePolicyDigest(old), RuntimeBefore: before}
	return RepositoryArchivePolicy{Schema: RepositoryArchivePolicySchema, OperationID: audit.OperationID, RepositoryUUID: old.RepositoryUUID, Revision: old.Revision, Epoch: 2, State: "releasing", StartedAt: audit.RequestedAt, Release: &audit}
}

func TestArchiveReleaseCanonicalAuditAndProjection(t *testing.T) {
	p, record, owner, guest := archivePolicyFixture(t)
	policy := archiveReleasePolicyFixture(record.RepoID)
	if err := p.publishArchivePolicy(t.Context(), record, policy); err != nil {
		t.Fatal(err)
	}
	if CheckCanonicalRepositoryWrite(p.ServiceWC, record.RepoID) == nil {
		t.Fatal("releasing opened early")
	}
	for _, client := range []string{owner, guest} {
		v, err := clientview.Load(filepath.Join(p.ServiceWC, "clients", client, "view.json"))
		if err != nil || v.Repositories[0].Access != "r" {
			t.Fatal(v, err)
		}
	}
	policy.State = "released"
	if err := p.publishArchivePolicy(t.Context(), record, policy); err != nil {
		t.Fatal(err)
	}
	if err := CheckCanonicalRepositoryWrite(p.ServiceWC, record.RepoID); err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{owner, guest} {
		v, err := clientview.Load(filepath.Join(p.ServiceWC, "clients", client, "view.json"))
		if err != nil || v.Repositories[0].Access != "rw" {
			t.Fatal(v, err)
		}
	}
	if err := p.RebuildGrantAuthority(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := CheckCanonicalRepositoryWrite(p.ServiceWC, record.RepoID); err != nil {
		t.Fatal("rebuild", err)
	}
	path, _ := canonicalArchiveReleasePath(p.ServiceWC, record.RepoID, policy.Epoch)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	audit := *policy.Release
	audit.Reason = "another reason"
	if _, err := writeCanonicalArchiveRelease(p.ServiceWC, record.RepoID, audit); err == nil {
		t.Fatal("overwrote canonical audit")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("audit bytes changed")
	}
	// A damaged or missing independent audit is not treated as a released repo.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if CheckCanonicalRepositoryWrite(p.ServiceWC, record.RepoID) == nil || p.RebuildGrantAuthority(t.Context()) == nil {
		t.Fatal("missing audit opened write authority")
	}
}

func TestArchiveReleasePolicyRejectsUnboundAndNestedAudit(t *testing.T) {
	for _, kind := range []string{"absent", "actor", "reason", "epoch", "digest", "runtime", "nested", "repo"} {
		t.Run(kind, func(t *testing.T) {
			p := archiveReleasePolicyFixture(uuid.NewString())
			audit := *p.Release
			p.Release = &audit
			switch kind {
			case "absent":
				p.Release = nil
			case "actor":
				audit.Actor = ""
			case "reason":
				audit.Reason = "line1\nline2"
			case "epoch":
				p.Epoch++
			case "digest":
				audit.PreviousSHA256 = "wrong"
			case "runtime":
				audit.RuntimeBefore.OperationID = uuid.NewString()
			case "nested":
				audit.Previous.Release = &ArchiveReleaseAudit{}
			case "repo":
				audit.RepoID = "../other"
			}
			if p.validate() == nil {
				t.Fatal("unbound release accepted")
			}
		})
	}
	// The wire cannot erase the audit by merely naming a new state.
	var p RepositoryArchivePolicy
	if err := json.Unmarshal([]byte(`{"schema":"filees.repository-archive-policy/v1","state":"released"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.validate() == nil {
		t.Fatal("released without authority")
	}
}

func TestArchiveReleaseDoesNotUpgradeReader(t *testing.T) {
	p, record, owner, guest := archivePolicyFixture(t)
	v, err := clientview.Load(filepath.Join(p.ServiceWC, "clients", guest, "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Grant(t.Context(), record.OwnerRealmID, v.RealmID, record.RepoID, "r"); err != nil {
		t.Fatal(err)
	}
	policy := archiveReleasePolicyFixture(record.RepoID)
	policy.State = "released"
	if err := p.publishArchivePolicy(t.Context(), record, policy); err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{owner, guest} {
		v, err := clientview.Load(filepath.Join(p.ServiceWC, "clients", client, "view.json"))
		if err != nil {
			t.Fatal(err)
		}
		want := "rw"
		if client == guest {
			want = "r"
		}
		if v.Repositories[0].Access != want {
			t.Fatal("release enlarged a reader grant", v)
		}
	}
}
