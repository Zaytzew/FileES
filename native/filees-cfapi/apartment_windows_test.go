//go:build windows && native_cfapi_probe

package nativecfapiprobe

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestShellDiagnosticAfterApartmentTeardown(t *testing.T) {
	probe := os.Getenv("FILEES_CFAPI_APARTMENT_PROBE")
	if probe == "" {
		t.Fatal("set FILEES_CFAPI_APARTMENT_PROBE to the opt-in lifetime probe")
	}
	raw, err := exec.Command(probe, t.TempDir()).CombinedOutput()
	if err != nil || bytes.Count(raw, []byte("round=")) != 32 {
		t.Fatalf("repeated WinRT diagnostic: %v\n%s", err, raw)
	}
	if !bytes.Contains(raw, []byte("crypto_loaded=0")) {
		t.Fatalf("probe did not exercise DLL unload: %s", raw)
	}
}

func TestInfoRejectsCorruptProviderMetadata(t *testing.T) {
	probe := os.Getenv("FILEES_CFAPI_APARTMENT_PROBE")
	if probe == "" {
		t.Fatal("set FILEES_CFAPI_APARTMENT_PROBE")
	}
	root := filepath.Join(t.TempDir(), "provider-corrupt")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if raw, err := exec.Command(probe, "--corrupt-provider", root).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, raw)
	}
	t.Cleanup(func() {
		if raw, err := exec.Command(probe, "--remove-corrupt-provider", root).CombinedOutput(); err != nil {
			t.Errorf("fixture unregister: %v %s", err, raw)
		}
	})
	got := run(t, "", "info", "--root", root)
	if got.OK || got.Error != "sync_root_boundary" || got.HResult != "0x80070166" {
		t.Fatalf("invalid provider presented as a valid foreign owner: %+v", got)
	}
}
