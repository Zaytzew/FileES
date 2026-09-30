package intake

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/obsandbox"
	"github.com/google/uuid"
)

func TestReceivingUnderSandbox(t *testing.T) {
	base := os.Getenv("FILEES_RECEIVING_SANDBOX")
	if base == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestReceivingUnderSandbox$")
		cmd.Env = append(os.Environ(), "FILEES_RECEIVING_SANDBOX="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sandbox: %v %s", err, out)
		}
		return
	}
	if err := obsandbox.Apply(obsandbox.Profile{Name: "intake-receiver-test", Promises: "stdio rpath wpath cpath fattr flock unix inet", Paths: []obsandbox.Path{{Label: "intake", Name: base, Perms: "rwc"}}}); err != nil {
		t.Fatal(err)
	}
	s := Store{Root: base, MaxBytes: 16, MaxQuarantineBytes: 16}
	id := uuid.NewString()
	lease, err := s.beginReceive(uuid.NewString(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, id, ".payload.tmp"), []byte("partial"), 0660); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	record, err := accept(t, s, uuid.NewString(), "next")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, id)); !os.IsNotExist(err) {
		t.Fatalf("orphan: %v", err)
	}
	if err := s.Claim(record.UploadID); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(record.UploadID); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(record.UploadID); err != nil {
		t.Fatal(err)
	}
}
