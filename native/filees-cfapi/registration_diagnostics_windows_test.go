//go:build windows && native_cfapi_probe

package nativecfapiprobe

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type registrationSnapshot struct {
	Schema      string `json:"schema"`
	Operation   string `json:"operation"`
	OperationID string `json:"operation_id"`
	Phase       string `json:"phase"`
	Healthy     bool   `json:"healthy"`
	Anomaly     bool   `json:"anomaly"`
	FileID      uint64 `json:"file_id"`
	Basic       struct {
		HR       uint32 `json:"hr"`
		Returned uint32 `json:"returned"`
		Raw      string `json:"raw_hex"`
	} `json:"basic"`
	Standard struct {
		HR       uint32 `json:"hr"`
		Returned uint32 `json:"returned"`
		Raw      string `json:"raw_hex"`
	} `json:"standard"`
}

func tracedRegistration(t *testing.T, args ...string) []registrationSnapshot {
	t.Helper()
	command := exec.Command(helper(t), args...)
	command.Env = append(os.Environ(), "FILEES_REGISTRATION_TRACE=sample", "FILEES_REGISTRATION_OPERATION=0123456789abcdef")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	var result answer
	if json.Unmarshal(raw, &result) != nil || err != nil || !result.OK {
		t.Fatalf("%v: %s %v stderr=%s", args, raw, err, stderr.Bytes())
	}
	var records []registrationSnapshot
	for _, line := range bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte{'\n'}) {
		if len(line) > 32768 {
			t.Fatal("unbounded diagnostic record")
		}
		var record registrationSnapshot
		if err := json.Unmarshal(line, &record); err != nil || record.Schema != "filees.registration-evidence/v1" || record.OperationID != "0123456789abcdef" {
			t.Fatalf("invalid diagnostic %s %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestRegistrationDiagnosticsNativeShellAndRelocation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "point")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	defer func() { run(t, "", "unregister", "--root", root) }()
	initial := tracedRegistration(t, "register", "--root", root, "--identity", "office\x1frepo")
	if len(initial) != 2 || initial[0].Phase != "before" || !initial[1].Healthy || initial[1].Basic.Returned != 8 {
		t.Fatalf("initial: %+v", initial)
	}
	id := initial[1].FileID
	// Remote bytes must stay remote throughout diagnostic reads and re-register.
	if got := run(t, "f\t123456\tremote.txt\tremote.txt\n", "placeholders", "--root", root); !got.OK {
		t.Fatal(got)
	}
	remoteBefore := fileStateProbe(t, root)
	icon := filepath.Join(t.TempDir(), "icon.ico")
	if err := os.WriteFile(icon, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rows := tracedRegistration(t, "shell-register", "--root", root, "--identity", "office\x1frepo", "--icon", icon, "--visible", "0")
		if len(rows) != 2 || rows[0].Operation != "shell_register" || !rows[0].Healthy || !rows[1].Healthy || rows[1].FileID != id {
			t.Fatalf("Shell round %d: %+v", i, rows)
		}
		rows = tracedRegistration(t, "register", "--root", root, "--identity", "office\x1frepo")
		if len(rows) != 2 || !rows[0].Healthy || !rows[1].Healthy {
			t.Fatalf("native round %d: %+v", i, rows)
		}
	}
	// Interrupted preparation: only part of a tree was seeded, with no connected
	// provider. Move and rollback must retain registration/FileID and remote data.
	target := filepath.Join(filepath.Dir(root), "moved")
	original := root
	if err := os.Rename(root, target); err != nil {
		t.Fatal(err)
	}
	root = target
	rows := tracedRegistration(t, "registration-snapshot", "--root", root, "--phase", "move_after")
	if len(rows) != 1 || !rows[0].Healthy || rows[0].FileID != id {
		t.Fatalf("move: %+v", rows)
	}
	if err := os.Rename(root, original); err != nil {
		t.Fatal(err)
	}
	root = original
	rows = tracedRegistration(t, "registration-snapshot", "--root", root, "--phase", "rollback")
	if len(rows) != 1 || !rows[0].Healthy || rows[0].FileID != id {
		t.Fatalf("rollback: %+v", rows)
	}
	remoteAfter := fileStateProbe(t, root)
	if !bytes.Equal(remoteBefore, remoteAfter) {
		t.Fatalf("diagnostics changed file metadata: before=%s after=%s", remoteBefore, remoteAfter)
	}
	rows = tracedRegistration(t, "unregister", "--root", root)
	if len(rows) != 4 || rows[0].Operation != "native_unregister" || rows[2].Operation != "shell_unregister" {
		t.Fatalf("detach phases: %+v", rows)
	}
	for _, row := range rows {
		if row.Anomaly {
			t.Fatal("normal detach classified as anomaly", row)
		}
	}
}

func fileStateProbe(t *testing.T, root string) []byte {
	t.Helper()
	raw, err := exec.Command(helper(t), "file-state", "--root", root, "--rel", "remote.txt").Output()
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Local  bool `json:"local"`
		InSync bool `json:"in_sync"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Local || !state.InSync {
		t.Fatalf("not remote: %s", raw)
	}
	return raw
}

func TestRegistrationDiagnosticsDisabledByDefault(t *testing.T) {
	root := anchor(t)
	command := exec.Command(helper(t), "info", "--root", root)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if _, err := command.Output(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected diagnostic IO: %s", stderr.Bytes())
	}
}
