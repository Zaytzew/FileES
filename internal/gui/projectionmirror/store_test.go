package projectionmirror

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"filees/pkg/privatefile"
)

func TestStoreUsesFullServerIDWithoutSanitizingCollisions(t *testing.T) {
	store := openTestStore(t)
	firstID := "spot:a/b"
	secondID := "spot_a_b"
	if fileName(firstID) == fileName(secondID) {
		t.Fatal("distinct full ServerIDs collide")
	}
	saveTestPayload(t, store, firstID, 1)
	saveTestPayload(t, store, secondID, 2)
	for serverID, want := range map[string]int{firstID: 1, secondID: 2} {
		entry, ok, err := store.Load(serverID)
		if err != nil || !ok {
			t.Fatalf("Load(%q) = ok %v, err %v", serverID, ok, err)
		}
		var payload struct {
			Value int `json:"value"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil || payload.Value != want {
			t.Fatalf("Load(%q) payload = %s, err %v", serverID, entry.Payload, err)
		}
	}
}

func TestStoreRejectsInvalidPayloadWithoutReplacingMirror(t *testing.T) {
	store := openTestStore(t)
	saveTestPayload(t, store, "spot", 1)
	if err := store.Save("spot", time.Now(), []byte(`{"partial":`)); err == nil {
		t.Fatal("invalid payload was accepted")
	}
	entry, ok, err := store.Load("spot")
	if err != nil || !ok || payloadValue(t, entry) != 1 {
		t.Fatalf("valid mirror was replaced: entry=%+v ok=%v err=%v", entry, ok, err)
	}
}

func TestCorruptMirrorIsIsolatedPerServer(t *testing.T) {
	store := openTestStore(t)
	saveTestPayload(t, store, "spot", 1)
	saveTestPayload(t, store, "cloud", 2)
	if err := os.WriteFile(store.path("cloud"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Load("cloud"); err == nil || ok {
		t.Fatalf("corrupt cloud mirror = ok %v, err %v", ok, err)
	}
	entry, ok, err := store.Load("spot")
	if err != nil || !ok || payloadValue(t, entry) != 1 {
		t.Fatalf("healthy spot mirror lost: entry=%+v ok=%v err=%v", entry, ok, err)
	}
}

func TestPruneRemovesInactiveMirrorsAndAbandonedTemporaryFiles(t *testing.T) {
	store := openTestStore(t)
	saveTestPayload(t, store, "spot", 1)
	saveTestPayload(t, store, "cloud", 2)
	temporary := filepath.Join(store.root, ".mirror-abandoned.tmp")
	if err := os.WriteFile(temporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := []string{
		filepath.Join(store.root, "operator-note.txt"),
		filepath.Join(store.root, "operator-note.json"),
	}
	for _, path := range unknown {
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.Prune([]string{"spot"})
	if err != nil || removed != 2 {
		t.Fatalf("Prune = %d, %v", removed, err)
	}
	if _, ok, err := store.Load("spot"); err != nil || !ok {
		t.Fatalf("active mirror removed: ok %v, err %v", ok, err)
	}
	if _, ok, err := store.Load("cloud"); err != nil || ok {
		t.Fatalf("inactive mirror survived: ok %v, err %v", ok, err)
	}
	for _, path := range unknown {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unknown file was touched: %v", err)
		}
	}
}

func TestConcurrentSavesAlwaysPublishACompletePrivateEntry(t *testing.T) {
	store := openTestStore(t)
	const writers = 40
	var group sync.WaitGroup
	for value := 0; value < writers; value++ {
		group.Add(1)
		go func() {
			defer group.Done()
			payload, _ := json.Marshal(map[string]int{"value": value})
			if err := store.Save("spot", time.Unix(int64(value+1), 0), payload); err != nil {
				t.Errorf("Save(%d): %v", value, err)
			}
		}()
	}
	group.Wait()
	entry, ok, err := store.Load("spot")
	if err != nil || !ok || !json.Valid(entry.Payload) {
		t.Fatalf("concurrent result = entry %+v, ok %v, err %v", entry, ok, err)
	}
	if err := privatefile.Verify(store.root); err != nil {
		t.Fatalf("store directory is not private: %v", err)
	}
	if err := privatefile.Verify(store.path("spot")); err != nil {
		t.Fatalf("published mirror is not private: %v", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "mirror"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func saveTestPayload(t *testing.T, store *Store, serverID string, value int) {
	t.Helper()
	payload, err := json.Marshal(map[string]int{"value": value})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(serverID, time.Unix(int64(value), 0), payload); err != nil {
		t.Fatal(err)
	}
}

func payloadValue(t *testing.T, entry Entry) int {
	t.Helper()
	var payload struct {
		Value int `json:"value"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Value
}
