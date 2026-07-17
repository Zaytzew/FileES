package state

import (
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
