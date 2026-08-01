package localpin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceInstanceIDPersistsAcrossCalls(t *testing.T) {
	root := t.TempDir()
	first, err := deviceInstanceID(root)
	if err != nil || first == "" {
		t.Fatalf("deviceInstanceID = %q, %v", first, err)
	}
	second, err := deviceInstanceID(root)
	if err != nil || second != first {
		t.Fatalf("deviceInstanceID not stable: first=%q second=%q err=%v", first, second, err)
	}
	info, err := os.Stat(filepath.Join(root, "device_id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("device_id mode=%v, want 0600", info.Mode().Perm())
	}
}

func TestDeviceInstanceIDDiffersPerRoot(t *testing.T) {
	a, err := deviceInstanceID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := deviceInstanceID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two independent roots produced the same device instance ID")
	}
}

func TestDeviceKeysPrimaryDoesNotDependOnMACAddress(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pin.json")

	original := firstMACAddress
	defer func() { firstMACAddress = original }()

	firstMACAddress = func() string { return "aa:bb:cc:dd:ee:ff" }
	before := deviceKeys(path)

	firstMACAddress = func() string { return "" } // NIC removed
	afterRemoved := deviceKeys(path)

	firstMACAddress = func() string { return "11:22:33:44:55:66" } // different NIC now enumerates first
	afterChanged := deviceKeys(path)

	if len(before) == 0 || len(afterRemoved) == 0 || len(afterChanged) == 0 {
		t.Fatalf("expected at least one candidate key in every scenario: %d %d %d", len(before), len(afterRemoved), len(afterChanged))
	}
	if string(before[0]) != string(afterRemoved[0]) || string(before[0]) != string(afterChanged[0]) {
		t.Fatal("primary key changed when only the MAC address changed - the stability fix regressed")
	}
}
