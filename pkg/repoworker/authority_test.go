package repoworker

import (
	"context"
	"encoding/json"
	"filees/pkg/clientview"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type publishRunner struct{ calls int }

func (r *publishRunner) Publish(context.Context, []string, string) error { r.calls++; return nil }
func TestServicePublisherProjectsOnlyOwnerRealmAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	realm, other := uuid.NewString(), uuid.NewString()
	ownerClient, otherClient := uuid.NewString(), uuid.NewString()
	writeView := func(client, realm string) {
		v := clientview.View{Schema: clientview.Schema, ClientID: client, RealmID: realm, Generation: 1, GeneratedAt: time.Now(), ClientRole: "normal", Capabilities: &clientview.Capabilities{CanCreateRepositories: true}, Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}
		if _, e := clientview.StoreIfNewer(filepath.Join(root, "clients", client, "view.json"), v); e != nil {
			t.Fatal(e)
		}
	}
	writeView(ownerClient, realm)
	writeView(otherClient, other)
	run := &publishRunner{}
	authz := filepath.Join(t.TempDir(), "data.authz")
	p := ServicePublisher{ServiceWC: root, DataAuthzFile: authz, Runner: run}
	repo := uuid.NewString()
	url := "svn+ssh://_filees-client@example/repos/" + repo
	if e := p.Publish(context.Background(), repo, realm, "Docs", url); e != nil {
		t.Fatal(e)
	}
	if e := p.Publish(context.Background(), repo, realm, "Docs", url); e != nil {
		t.Fatal(e)
	}
	owner, _ := clientview.Load(filepath.Join(root, "clients", ownerClient, "view.json"))
	foreign, _ := clientview.Load(filepath.Join(root, "clients", otherClient, "view.json"))
	if owner.Generation != 2 || len(owner.Repositories) != 1 || foreign.Generation != 1 || len(foreign.Repositories) != 0 {
		t.Fatalf("owner=%+v foreign=%+v", owner, foreign)
	}
	if owner.Repositories[0].State != "initializing" {
		t.Fatalf("new repository state = %q", owner.Repositories[0].State)
	}
	if err := p.Activate(context.Background(), repo, other); err == nil {
		t.Fatal("foreign realm activated repository")
	}
	if err := p.Activate(context.Background(), repo, realm); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate(context.Background(), repo, realm); err != nil {
		t.Fatalf("idempotent activation failed: %v", err)
	}
	owner, _ = clientview.Load(filepath.Join(root, "clients", ownerClient, "view.json"))
	if owner.Generation != 3 || owner.Repositories[0].State != "active" {
		t.Fatalf("activated owner projection = %+v", owner)
	}
	raw, _ := os.ReadFile(authz)
	if !strings.Contains(string(raw), ownerClient) || strings.Contains(string(raw), otherClient) {
		t.Fatalf("authz=%s", raw)
	}
}
