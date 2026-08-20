package whaleclient

import (
	"path/filepath"
	"testing"
	"time"

	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
)

func TestSpoolSelectionPrefersAnotherPhysicalDeviceThenLargestRemainder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "control")
	manager, err := NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SpoolSourceIdentity = func(string) (string, string, string, error) {
		return `D:\`, "volume-source", "disk:1", nil
	}
	base := t.TempDir()
	manager.SpoolCandidates = func(string) ([]spoolCandidate, error) {
		return []spoolCandidate{
			{Root: filepath.Join(base, "same"), VolumeID: "volume-same", DeviceID: "disk:1", Available: 10 << 30},
			{Root: filepath.Join(base, "other-small"), VolumeID: "volume-b", DeviceID: "disk:0", Available: 2 << 30},
			{Root: filepath.Join(base, "other-large"), VolumeID: "volume-c", DeviceID: "disk:2", Available: 3 << 30},
		}, nil
	}

	selection, err := manager.selectSpool(`D:\source.pst`, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if selection.VolumeID != "volume-c" || selection.DeviceID != "disk:2" {
		t.Fatalf("selection=%+v", selection)
	}
}

func TestSpoolSelectionUsesStableVolumeIDTieBreak(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "control"), nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SpoolSourceIdentity = func(string) (string, string, string, error) {
		return `D:\`, "volume-source", "disk:1", nil
	}
	base := t.TempDir()
	manager.SpoolCandidates = func(string) ([]spoolCandidate, error) {
		return []spoolCandidate{
			{Root: filepath.Join(base, "z"), VolumeID: "volume-z", DeviceID: "disk:2", Available: 3 << 30},
			{Root: filepath.Join(base, "a"), VolumeID: "volume-a", DeviceID: "disk:0", Available: 3 << 30},
		}, nil
	}

	selection, err := manager.selectSpool(`D:\source.pst`, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if selection.VolumeID != "volume-a" {
		t.Fatalf("selection=%+v", selection)
	}
}

func TestSpoolSelectionSubtractsOutstandingReservations(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "control"), nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SpoolSourceIdentity = func(string) (string, string, string, error) {
		return `D:\`, "volume-source", "disk:1", nil
	}
	base := t.TempDir()
	reservedRoot := filepath.Join(base, "reserved")
	fallbackRoot := filepath.Join(base, "fallback")
	manager.SpoolCandidates = func(string) ([]spoolCandidate, error) {
		return []spoolCandidate{
			{Root: reservedRoot, VolumeID: "volume-reserved", DeviceID: "disk:2", Available: 3 << 30},
			{Root: fallbackRoot, VolumeID: "volume-fallback", DeviceID: "disk:3", Available: 5 << 29},
		}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	reservation := Operation{
		Schema: OperationSchema, OperationID: uuid.NewString(), ServerID: "office", Direction: whale.DirectionPut,
		LogicalRepoID: uuid.NewString(), LogicalPath: "media/reserved.bin", GenerationID: uuid.NewString(),
		SourcePath: filepath.Join(base, "reserved.bin"), SpoolRoot: reservedRoot, SpoolVolumeID: "volume-reserved",
		SpoolDeviceID: "disk:2", ReservedBytes: 2 << 30, State: StatePreparing, CreatedAt: now, UpdatedAt: now,
	}
	if err := manager.save(reservation); err != nil {
		t.Fatal(err)
	}

	selection, err := manager.selectSpool(`D:\source.pst`, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if selection.VolumeID != "volume-fallback" {
		t.Fatalf("selection=%+v", selection)
	}
}
