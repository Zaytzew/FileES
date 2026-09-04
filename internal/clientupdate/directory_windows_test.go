//go:build windows

package clientupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// sleepEnv makes a copy of this test binary sit still instead of running tests,
// so the copy can be a genuinely running executable.
const sleepEnv = "FILEES_DIRECTORY_INSTALLER_SLEEP"

func TestMain(m *testing.M) {
	if os.Getenv(sleepEnv) != "" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The claim the whole installer rests on, measured rather than assumed.
//
// Windows refuses to overwrite or delete an executable while it runs, and the
// daemon applying an update is running its own. It does allow the running image
// to be renamed - that is how a self-updating program on Windows replaces
// itself - so the old file is moved aside and the new one takes its place.
//
// An earlier version of this test stood in for "running" with an os.Open
// handle, and failed. That was the test being wrong, not the code: Go opens
// without FILE_SHARE_DELETE, so an ordinary handle blocks a rename that a
// running image permits. The two are not the same thing, and only one of them
// is what happens in production. So this one runs an actual process from the
// file it then replaces - the test binary copied aside and told to sleep.
func TestARunningExecutableIsReplacedByMovingItAside(t *testing.T) {
	installDir := t.TempDir()
	target := filepath.Join(installDir, "filees.exe")

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, image, 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(target)
	command.Env = append(os.Environ(), sleepEnv+"=1")
	if err := command.Start(); err != nil {
		t.Fatalf("start the copy: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	// Give the loader time to map the image, so the rename really is happening
	// against a running executable rather than winning a race with startup.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if handle, err := os.Open(target); err == nil {
			handle.Close()
		}
		if command.Process != nil && processIsAlive(command.Process.Pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the copied binary never started")
		}
		time.Sleep(50 * time.Millisecond)
	}

	installer := newInstaller(installDir, "")
	if err := installer.replace(target, []byte("daemon new"), "20260904-020000"); err != nil {
		t.Fatalf("replace a running executable: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "daemon new" {
		t.Fatalf("target = %q, want the new content", got)
	}
	aside := target + supersededSuffix + "20260904-020000"
	if _, err := os.Stat(aside); err != nil {
		t.Fatalf("the running image was not moved aside: %v", err)
	}
	// And it is still running, from a file that now has a different name -
	// which is what lets the daemon finish the update it is applying to itself.
	if !processIsAlive(command.Process.Pid) {
		t.Error("the process died when its image was renamed")
	}
}

func processIsAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	// STILL_ACTIVE. A process that has exited reports its real exit code, so
	// this distinguishes running from finished rather than from unknown.
	return code == 259
}
