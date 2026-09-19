package repoworker

import (
	"bytes"
	"context"
	"encoding/json"
	control "filees/pkg/control/v1"
	"filees/pkg/guiblob"
	"github.com/google/uuid"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestGUIBlobClonesCASAndRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "blobs")
	realm := uuid.NewString()
	first := GUIBlobStore{Root: root}
	second := GUIBlobStore{Root: root}
	initial, err := first.Exchange(t.Context(), realm, nil)
	if err != nil || initial.Version != "" {
		t.Fatal(initial, err)
	}
	// Arbitrary UTF-8, not drawer JSON: the server must not interpret the document.
	saved, err := first.Exchange(t.Context(), realm, &guiblob.Write{Data: "opaque layout"})
	if err != nil || saved.Version == "" {
		t.Fatal(saved, err)
	}
	clone, err := second.Exchange(t.Context(), realm, nil)
	if err != nil || clone != saved {
		t.Fatal(clone, err)
	}
	stale, err := second.Exchange(t.Context(), realm, &guiblob.Write{Data: "lost update"})
	if err != nil || !stale.Conflict || stale.Data != saved.Data {
		t.Fatal(stale, err)
	}
	restarted, err := (GUIBlobStore{Root: root}).Exchange(t.Context(), realm, nil)
	if err != nil || restarted != saved {
		t.Fatal(restarted, err)
	}
	other, err := first.Exchange(t.Context(), uuid.NewString(), nil)
	if err != nil || other.Data != "" {
		t.Fatal(other, err)
	}
	if _, err = first.Exchange(t.Context(), "../outside", nil); err == nil {
		t.Fatal("accepted realm traversal")
	}
}

func TestGUIBlobConcurrentCAS(t *testing.T) {
	if !FileLocksSupported() {
		t.Skip("server flock acceptance is Unix")
	}
	store := GUIBlobStore{Root: filepath.Join(t.TempDir(), "state")}
	realm := uuid.NewString()
	results := make(chan guiblob.State, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, data := range []string{"one", "two"} {
		wg.Add(1)
		go func(data string) {
			defer wg.Done()
			state, err := store.Exchange(context.Background(), realm, &guiblob.Write{Data: data})
			results <- state
			errs <- err
		}(data)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	conflicts := 0
	for state := range results {
		if state.Conflict {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflicts=%d", conflicts)
	}
}

type guiBlobResolver map[string]Session

func (r guiBlobResolver) Resolve(id string) (Session, error) { return r[id], nil }

func TestGUIBlobDispatcherUsesAuthenticatedRealm(t *testing.T) {
	realm, other := uuid.NewString(), uuid.NewString()
	a, b, c, phone := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	results, err := NewFileStore(filepath.Join(t.TempDir(), "results"))
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{Store: results, GUIBlobs: &GUIBlobStore{Root: filepath.Join(t.TempDir(), "state")}}
	dispatcher := Dispatcher{Worker: worker, Resolver: guiBlobResolver{
		a:     {ClientID: a, RealmID: realm, CanCreateRepositories: true},
		b:     {ClientID: b, RealmID: realm, CanCreateRepositories: true},
		c:     {ClientID: c, RealmID: other, CanCreateRepositories: true},
		phone: {ClientID: phone, RealmID: realm},
	}}
	exchange := func(id string, typ control.TicketType, payload any) control.Result {
		t.Helper()
		ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), typ, id, payload, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(ticket)
		var out bytes.Buffer
		if err = dispatcher.Serve(t.Context(), id, bytes.NewReader(raw), &out); err != nil {
			t.Fatal(err)
		}
		result, err := control.ParseResult(out.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := exchange(a, control.TicketSetGUIBlob, guiblob.Write{Data: "drawer data"})
	if result.Status != control.ResultOK {
		t.Fatal(result)
	}
	result = exchange(b, control.TicketGetGUIBlob, struct{}{})
	var state guiblob.State
	if err := control.DecodeResultPayload(result.Result, &state); err != nil || state.Data != "drawer data" {
		t.Fatal(state, err)
	}
	result = exchange(c, control.TicketGetGUIBlob, struct{}{})
	if err := control.DecodeResultPayload(result.Result, &state); err != nil || state.Data != "" {
		t.Fatal(state, err)
	}
	result = exchange(phone, control.TicketGetGUIBlob, struct{}{})
	if result.Status == control.ResultOK {
		t.Fatal("read-only session got desktop GUI state")
	}
	_, err = control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketGetGUIBlob, a, map[string]string{"realm_id": other}, time.Now())
	if err == nil {
		t.Fatal("accepted a payload-selected realm")
	}
}
