//go:build windows

package whaleclient

import (
	"os"
	"path/filepath"
	"testing"
)

// This probe is opt-in because it enumerates real fixed volumes. It is useful
// on deployment machines for proving that partitions sharing one physical
// disk are not mistaken for independent spool devices.
func TestSystemSpoolSelectionProbe(t *testing.T) {
	source := os.Getenv("FILEES_WHALE_SOURCE_PROBE")
	if source == "" {
		t.Skip("FILEES_WHALE_SOURCE_PROBE is not set")
	}
	manager, err := NewManager(filepath.Join(t.TempDir(), "control"), nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.EnableSystemSpoolSelection()
	selection, err := manager.selectSpool(source, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	_, sourceVolume, sourceDevice, err := systemPathIdentity(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source_volume=%s source_device=%s spool_volume=%s spool_device=%s spool_root=%s", sourceVolume, sourceDevice, selection.VolumeID, selection.DeviceID, selection.Root)
	if selection.DeviceID == sourceDevice {
		t.Fatal("allocator selected the source physical device despite another eligible device")
	}
	if filepath.Clean(selection.Root) != filepath.Clean(manager.Root) {
		t.Cleanup(func() { _ = os.Remove(selection.Root) })
	}
}
