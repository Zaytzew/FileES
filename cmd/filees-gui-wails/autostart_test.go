package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
)

func TestManageAutostartUsesAbsoluteExecutableAndSocket(t *testing.T) {
	backend := &platformtest.Fake{}
	spec := newAutostartSpec(filepath.Join(t.TempDir(), "filees-gui"), "/run/user/1000/filees.sock")
	if err := manageAutostart(context.Background(), backend, "enable", spec, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	snapshot := backend.Snapshot()
	if len(snapshot.AutostartSets) != 1 || !snapshot.AutostartSets[0].Enabled {
		t.Fatalf("autostart calls = %#v", snapshot.AutostartSets)
	}
	got := snapshot.AutostartSets[0].Spec
	if !filepath.IsAbs(got.Executable) || got.ID != "filees-gui" {
		t.Fatalf("autostart spec = %#v", got)
	}
	if len(got.Args) != 2 || got.Args[0] != "--socket" || got.Args[1] != "/run/user/1000/filees.sock" {
		t.Fatalf("autostart args = %#v", got.Args)
	}
}

func TestManageAutostartStatusAndDisable(t *testing.T) {
	backend := &platformtest.Fake{
		AutostartStatusFunc: func(context.Context, platform.AutostartSpec) (platform.AutostartState, error) {
			return platform.AutostartState{Enabled: true, Current: true, Source: "test-source"}, nil
		},
	}
	spec := newAutostartSpec(filepath.Join(t.TempDir(), "filees-gui"), "/tmp/filees.sock")
	var output bytes.Buffer
	if err := manageAutostart(context.Background(), backend, "status", spec, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "autostart: enabled (test-source)\n" {
		t.Fatalf("status output = %q", output.String())
	}
	if err := manageAutostart(context.Background(), backend, "disable", spec, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	snapshot := backend.Snapshot()
	if len(snapshot.AutostartSets) != 1 || snapshot.AutostartSets[0].Enabled {
		t.Fatalf("autostart calls = %#v", snapshot.AutostartSets)
	}
}

func TestManageAutostartRejectsUnknownMode(t *testing.T) {
	backend := &platformtest.Fake{}
	if err := manageAutostart(context.Background(), backend, "surprise", platform.AutostartSpec{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown mode error")
	}
	if snapshot := backend.Snapshot(); len(snapshot.AutostartSets) != 0 || len(snapshot.StatusRequests) != 0 {
		t.Fatalf("backend called for invalid mode: %#v", snapshot)
	}
}

// The Linux installer runs the client to enable autostart, and the AppImage
// runs the installer on first launch. When the flag it passes is not the flag
// this binary defines, the installation ends on a usage message and the client
// never starts - which is exactly how it shipped in 0.1.16.1352. The script is
// the source of truth here: whatever it invokes has to be served.
func TestTheLinuxInstallerInvokesAFlagThisBinaryServes(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "packaging", "linux", "install-user.sh"))
	if err != nil {
		t.Fatal(err)
	}
	invocation := regexp.MustCompile(`"\$gui_bin"\s+--([a-z-]+)\s+([a-z]+)`).FindSubmatch(script)
	if invocation == nil {
		t.Fatal("install-user.sh no longer runs the client for autostart; drop this test with the call")
	}
	flag, mode := string(invocation[1]), string(invocation[2])

	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(main, []byte(`flags.String("`+flag+`"`)) {
		t.Fatalf("install-user.sh passes --%s, which the client does not define", flag)
	}
	if err := manageAutostart(context.Background(), &platformtest.Fake{}, mode, newAutostartSpec(filepath.Join(t.TempDir(), "filees-gui"), "/tmp/filees.sock"), &bytes.Buffer{}); err != nil {
		t.Fatalf("install-user.sh passes --%s %s, which the client refuses: %v", flag, mode, err)
	}
}
