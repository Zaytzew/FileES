package clientupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"filees/internal/releaseenvelope"
)

func TestStateRejectsRollbackAndSequenceForkButAllowsIdempotence(t *testing.T) {
	state := State{Schema: stateSchema, HighestSequence: 10, SecurityEpoch: 3, ReleaseID: "r10", InstalledVersion: "1.0"}
	valid := &releaseenvelope.Envelope{ReleaseID: "r10", Sequence: 10, SecurityEpoch: 3}
	if err := state.Check(valid); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*releaseenvelope.Envelope{
		{ReleaseID: "r9", Sequence: 9, SecurityEpoch: 3},
		{ReleaseID: "fork", Sequence: 10, SecurityEpoch: 3},
		{ReleaseID: "r11", Sequence: 11, SecurityEpoch: 2},
	} {
		if err := state.Check(candidate); err == nil {
			t.Fatalf("accepted rollback/fork: %+v", candidate)
		}
	}
	advanced, err := state.Advance(&releaseenvelope.Envelope{ReleaseID: "r11", Sequence: 11, SecurityEpoch: 4}, "1.1")
	if err != nil || advanced.HighestSequence != 11 || advanced.SecurityEpoch != 4 || advanced.InstalledVersion != "1.1" {
		t.Fatalf("advanced = %+v, %v", advanced, err)
	}
}

func TestStateStoreIsStrictPrivateAndDurableShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "update.json")
	store := StateStore{Path: path}
	want := State{HighestSequence: 12, SecurityEpoch: 2, ReleaseID: "r12", InstalledVersion: "1.2"}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.HighestSequence != want.HighestSequence || got.ReleaseID != want.ReleaseID {
		t.Fatalf("loaded = %+v, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The mode is only meaningful where it means something. Go reports 0666
	// for any writable file on Windows regardless of what Chmod was asked for,
	// so asserting 0600 there fails without telling us anything. The state file
	// holds update sequence numbers rather than secrets, so unlike the recovery
	// kit it does not warrant a DACL check in its place.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v", info.Mode())
	}
	if err := os.WriteFile(path, []byte(`{"schema":1,"highest_sequence":1,"security_epoch":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("accepted unknown state field")
	}
}
