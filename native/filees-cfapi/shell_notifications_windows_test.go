//go:build windows && native_cfapi_probe

package nativecfapiprobe

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

type shellEvent struct {
	Event    int  `json:"shell_event"`
	Flags    int  `json:"flags"`
	Targeted bool `json:"targeted"`
}

func shellProbe(t *testing.T, args ...string) (answer, []shellEvent) {
	t.Helper()
	command := exec.Command(helper(t), args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	var result answer
	if json.Unmarshal(raw, &result) != nil || (err != nil && result.OK) {
		t.Fatalf("%v: %s %v stderr=%s", args, raw, err, stderr.Bytes())
	}
	var events []shellEvent
	for _, line := range bytes.Split(bytes.TrimSpace(stderr.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event shellEvent
		if json.Unmarshal(line, &event) != nil || !event.Targeted || event.Event == 0x08000000 {
			t.Fatalf("global or invalid Shell notification: %s", line)
		}
		events = append(events, event)
	}
	return result, events
}

// Includes registry write times: equal visibility must not rewrite already-
// correct DWORDs or namespace links. SDK Register remains a separate operation.
func shellRegistrationStamp(t *testing.T, root string) string {
	t.Helper()
	for _, hive := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if stamp := shellRegistrationStampHive(t, root, hive); stamp != "" {
			return stamp
		}
	}
	t.Fatal("fixture Shell registration missing")
	return ""
}

func shellRegistrationStampHive(t *testing.T, root string, hive registry.Key) string {
	t.Helper()
	base := `Software\Microsoft\Windows\CurrentVersion\Explorer\SyncRootManager`
	parent, err := registry.OpenKey(hive, base, registry.READ)
	if err == registry.ErrNotExist {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	names, err := parent.ReadSubKeyNames(-1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "FileES!") {
			continue
		}
		key, err := registry.OpenKey(parent, name+`\UserSyncRoots`, registry.READ)
		if err == registry.ErrNotExist {
			continue
		}
		if err != nil {
			t.Fatalf("UserSyncRoots %s: %v", name, err)
		}
		values, err := key.ReadValueNames(-1)
		if err != nil {
			key.Close()
			t.Fatal(err)
		}
		found := false
		for _, value := range values {
			stored, _, err := key.GetStringValue(value)
			found = found || (err == nil && strings.EqualFold(filepath.Clean(stored), filepath.Clean(root)))
		}
		key.Close()
		if !found {
			continue
		}
		registration, err := registry.OpenKey(parent, name, registry.READ)
		if err != nil {
			t.Fatal(err)
		}
		clsid, _, err := registration.GetStringValue("NamespaceCLSID")
		registration.Close()
		if err != nil {
			t.Fatal(err)
		}
		stamp := name + "|" + clsid
		for _, path := range []string{base + `\` + name, `Software\Classes\CLSID\` + clsid,
			`Software\Classes\CLSID\` + clsid + `\ShellFolder`,
			`Software\Microsoft\Windows\CurrentVersion\Explorer\Desktop\NameSpace\` + clsid} {
			partHive := registry.CURRENT_USER
			if path == base+`\`+name {
				partHive = hive
			}
			part, err := registry.OpenKey(partHive, path, registry.READ)
			if err == registry.ErrNotExist {
				stamp += "|missing"
				continue
			}
			if err != nil {
				t.Fatalf("stamp %s: %v", path, err)
			}
			info, err := part.Stat()
			part.Close()
			if err != nil {
				t.Fatal(err)
			}
			stamp += "|" + info.ModTime().String()
		}
		return stamp
	}
	return ""
}

func TestShellNotificationsQuietVisibilityAndTargetedRegistration(t *testing.T) {
	probe := os.Getenv("FILEES_CFAPI_SHELL_NOTIFY_PROBE")
	if probe == "" {
		t.Fatal("set FILEES_CFAPI_SHELL_NOTIFY_PROBE to the unshipped recorder")
	}
	t.Setenv("FILEES_CFAPI", probe)
	root := anchor(t)
	icon := filepath.Join(t.TempDir(), "icon.ico")
	if err := os.WriteFile(icon, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := run(t, "f\t123456\tremote.txt\tremote.txt\n", "placeholders", "--root", root); !got.OK {
		t.Fatal(got)
	}
	remote := fileStateProbe(t, root)
	register := []string{"shell-register", "--root", root, "--identity", "synthetic-test", "--icon", icon, "--visible", "0"}
	if result, _ := shellProbe(t, register...); !result.OK {
		t.Fatal(result)
	}
	previous := "0"
	for _, visible := range []string{"0", "1", "0", "1", "0"} {
		result, events := shellProbe(t, "shell-visibility", "--root", root, "--visible", visible)
		if !result.OK {
			t.Fatal(result)
		}
		for _, event := range events {
			want := 0x10 // SHCNE_RMDIR
			if visible == "1" {
				want = 0x8 // SHCNE_MKDIR
			}
			if event.Event != want {
				t.Fatalf("unexpected namespace event: %+v", event)
			}
		}
		wantEvents := 0
		if previous != visible {
			wantEvents = 1
		}
		if len(events) != wantEvents {
			t.Fatalf("visibility %s to %s: got %+v, want %d events", previous, visible, events, wantEvents)
		}
		previous = visible
		stamp := shellRegistrationStamp(t, root)
		for i := 0; i < 4; i++ {
			result, events := shellProbe(t, "shell-visibility", "--root", root, "--visible", visible)
			if !result.OK || len(events) != 0 {
				t.Fatalf("no-op visibility %s notified or failed: %+v %+v", visible, result, events)
			}
			if after := shellRegistrationStamp(t, root); after != stamp {
				t.Fatalf("no-op rewrote registration: before=%s after=%s", stamp, after)
			}
		}
	}
	// Register may recreate a namespace on this Windows, including identical
	// input; notifications must still be targeted, never global/desktop refresh.
	for i := 0; i < 8; i++ {
		if result, events := shellProbe(t, register...); !result.OK || len(events) > 1 {
			t.Fatalf("re-register failed or broadcast multiple notifications: %+v %+v", result, events)
		}
	}
	// Genuine icon-resource changes still reach the SDK; do not blindly skip
	// Register based on a local cache when the supported readback omits the root.
	register[len(register)-3] = filepath.Join(t.TempDir(), "changed.ico")
	before := shellRegistrationStamp(t, root)
	if result, _ := shellProbe(t, register...); !result.OK {
		t.Fatal(result)
	}
	after := shellRegistrationStamp(t, root)
	if after == before {
		t.Fatal("changed icon did not update registration")
	}
	if result, events := shellProbe(t, "shell-visibility", "--root", root, "--visible", "0"); !result.OK || len(events) != 0 || shellRegistrationStamp(t, root) != after {
		t.Fatalf("updated visibility not idempotent: %+v %+v", result, events)
	}
	if got := fileStateProbe(t, root); !bytes.Equal(remote, got) {
		t.Fatalf("remote placeholder changed: before=%s after=%s", remote, got)
	}
	ordinary := t.TempDir()
	if result, events := shellProbe(t, "shell-visibility", "--root", ordinary, "--visible", "1"); result.OK || len(events) > 0 {
		t.Fatalf("unowned root accepted or notified: %+v %+v", result, events)
	}
}
