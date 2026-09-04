package repoworker

import (
	"bytes"
	"context"
	"encoding/json"
	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"github.com/google/uuid"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestDispatcherResolvesAuthorityFromView(t *testing.T) {
	root := t.TempDir()
	clientID, realm := uuid.NewString(), uuid.NewString()
	v := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Serwer testowy", ClientID: clientID, RealmID: realm, Generation: 1, GeneratedAt: time.Now(), ClientRole: "normal", Capabilities: &clientview.Capabilities{CanCreateRepositories: true}, Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}
	if _, e := clientview.StoreIfNewer(filepath.Join(root, "clients", clientID, "view.json"), v); e != nil {
		t.Fatal(e)
	}
	tk, e := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketCreateRepository, clientID, control.CreateRepositoryPayload{Name: "Docs"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(tk)
	backend := &fakeBackend{}
	store, _ := NewFileStore(filepath.Join(t.TempDir(), "results"))
	d := Dispatcher{Worker: &Worker{Backend: backend, Store: store}, Resolver: ViewResolver{ServiceWC: root}}
	var out bytes.Buffer
	if e = d.Serve(context.Background(), clientID, bytes.NewReader(raw), &out); e != nil {
		t.Fatal(e)
	}
	if backend.realm != realm || backend.calls != 1 {
		t.Fatalf("realm=%s calls=%d", backend.realm, backend.calls)
	}
}
func TestDispatcherRejectsOversizedTicket(t *testing.T) {
	d := Dispatcher{}
	if e := d.Serve(context.Background(), "client", bytes.NewReader(make([]byte, MaxTicketBytes+1)), io.Discard); e == nil {
		t.Fatal("oversized ticket accepted")
	}
}
