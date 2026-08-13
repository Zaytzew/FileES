package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingIsEmptyFirstInstall(t *testing.T) {
	st, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsFirstInstall() {
		t.Fatal("empty state must report first install")
	}
	if st.InstalledRelease != "" || len(st.History) != 0 {
		t.Fatalf("expected zero state, got %+v", st)
	}
	if !st.CanAdopt() {
		t.Fatal("empty state must be adoptable")
	}
}

func TestCanAdoptOnlyPristineState(t *testing.T) {
	cases := []*State{
		{InstalledRelease: "r1"},
		{InstalledAt: "now"},
		{HighestSequence: 1},
		{SecurityEpoch: 1},
		{System: &SystemState{Adopted: true}},
		{History: []HistoryEntry{{ReleaseID: "r1"}}},
	}
	for i, st := range cases {
		if st.CanAdopt() {
			t.Errorf("state %d unexpectedly adoptable: %+v", i, st)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := &State{
		InstalledRelease: "r3",
		InstalledAt:      "2026-07-17T00:00:00Z",
		System: &SystemState{
			UsersCreated: []string{"_filees-state"},
			SSHDFragment: "/etc/ssh/sshd_config.d/filees.conf",
			SSHDBackup:   "/etc/ssh/sshd_config.filees-before-install",
			SetuidBins:   []string{"/usr/local/sbin/filees-entry"},
		},
		History: []HistoryEntry{{
			ReleaseID:   "r3",
			InstalledAt: "2026-07-17T00:00:00Z",
			BackupDir:   "/var/filees/install-backup/x",
			Files: []BackupFile{{
				Target:       "/usr/local/sbin/filees-entry",
				BackupPath:   "/var/filees/install-backup/x/usr/local/sbin/filees-entry",
				Existed:      true,
				SHA256Before: "ab",
			}},
		}},
	}
	if err := Save(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out.IsFirstInstall() {
		t.Fatal("state with System must not report first install")
	}
	if out.InstalledRelease != "r3" || len(out.History) != 1 || len(out.History[0].Files) != 1 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if out.System.SSHDBackup != in.System.SSHDBackup {
		t.Fatalf("system state mismatch: %+v", out.System)
	}
}

func TestTransactionRoundTripAndRemoval(t *testing.T) {
	dir := t.TempDir()
	uid, gid := 100, 200
	in := &Transaction{Entry: HistoryEntry{
		ReleaseID: "r4", InstalledAt: "2026-08-13T00:00:00Z", BackupDir: "/backup/r4",
		Files: []BackupFile{{Target: "/bin/x", Existed: true, UIDBefore: &uid, GIDBefore: &gid}},
	}}
	if err := SaveTransaction(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadTransaction(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || out.Entry.ReleaseID != "r4" || len(out.Entry.Files) != 1 {
		t.Fatalf("transaction mismatch: %+v", out)
	}
	if err := RemoveTransaction(dir); err != nil {
		t.Fatal(err)
	}
	if out, err := LoadTransaction(dir); err != nil || out != nil {
		t.Fatalf("transaction still present: %+v, %v", out, err)
	}
}

func TestSaveIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, &State{InstalledRelease: "r1"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.json mode = %o, want 600", fi.Mode().Perm())
	}
	if _, err := os.Stat(Path(dir) + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

func TestLoadCorruptFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("corrupt state.json must fail loudly, not reset to first install")
	}
}

// TestFreshnessRejectsRollback is the regression test for the audit's Finding
// E. It follows the finding's own required matrix: forward upgrades pass, any
// backwards move is refused, a re-offer of the installed release is a
// controlled no-op, and a stale security epoch loses even when it carries a
// higher release sequence.
func TestFreshnessRejectsRollback(t *testing.T) {
	installed := &State{InstalledRelease: "v11", HighestSequence: 11, SecurityEpoch: 3}

	t.Run("upgrade 11->12 passes", func(t *testing.T) {
		if err := installed.CheckFreshness(12, 3, "v12"); err != nil {
			t.Fatalf("forward upgrade refused: %v", err)
		}
	})
	t.Run("downgrade 11->10 refused", func(t *testing.T) {
		err := installed.CheckFreshness(10, 3, "v10")
		if err == nil {
			t.Fatal("downgrade accepted")
		}
		if !errors.Is(err, ErrRollback) {
			t.Fatalf("downgrade error is not ErrRollback: %v", err)
		}
	})
	t.Run("same release re-offered is a no-op", func(t *testing.T) {
		if err := installed.CheckFreshness(11, 3, "v11"); err != nil {
			t.Fatalf("re-offer of the installed release refused: %v", err)
		}
	})
	t.Run("same sequence, different release is refused", func(t *testing.T) {
		if err := installed.CheckFreshness(11, 3, "v11-forged"); err == nil {
			t.Fatal("sequence fork accepted")
		}
	})
	t.Run("higher security epoch passes", func(t *testing.T) {
		if err := installed.CheckFreshness(12, 4, "v12"); err != nil {
			t.Fatalf("security epoch bump refused: %v", err)
		}
	})
	t.Run("older epoch loses despite a higher sequence", func(t *testing.T) {
		if err := installed.CheckFreshness(99, 2, "v99"); err == nil {
			t.Fatal("stale security epoch accepted because the sequence was higher")
		}
	})
	t.Run("missing counters fail closed", func(t *testing.T) {
		if err := installed.CheckFreshness(0, 0, "v-unversioned"); err == nil {
			t.Fatal("release without freshness counters accepted")
		}
		fresh := &State{}
		if err := fresh.CheckFreshness(0, 0, "v1"); err == nil {
			t.Fatal("first install accepted a release without freshness counters")
		}
	})
	t.Run("first install accepts any counted release", func(t *testing.T) {
		fresh := &State{}
		if err := fresh.CheckFreshness(1, 1, "v1"); err != nil {
			t.Fatalf("first install refused: %v", err)
		}
	})
}

func TestAdvanceFreshnessNeverMovesBackwards(t *testing.T) {
	st := &State{HighestSequence: 11, SecurityEpoch: 3}
	st.AdvanceFreshness(10, 2)
	if st.HighestSequence != 11 || st.SecurityEpoch != 3 {
		t.Fatalf("counters moved backwards: %+v", st)
	}
	st.AdvanceFreshness(12, 4)
	if st.HighestSequence != 12 || st.SecurityEpoch != 4 {
		t.Fatalf("counters did not advance: %+v", st)
	}
}

// TestCorruptStateFailsClosed covers the finding's "uszkodzony stan lokalny
// powoduje fail closed" requirement: an unreadable anti-rollback state must
// stop the update, never silently reset the floor to zero.
func TestCorruptStateFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("corrupt state loaded without error; the rollback floor would silently reset to zero")
	}
}

// TestSaveRoundTripsFreshness makes sure the counters actually survive a
// save/load cycle - the check is worthless if the state does not persist.
func TestSaveRoundTripsFreshness(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, &State{InstalledRelease: "v12", HighestSequence: 12, SecurityEpoch: 4}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.HighestSequence != 12 || got.SecurityEpoch != 4 {
		t.Fatalf("freshness did not persist: %+v", got)
	}
	if err := got.CheckFreshness(11, 4, "v11"); err == nil {
		t.Fatal("reloaded state accepted a rollback")
	}
}
