package localallocation

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestSparseAllocationIsNotLogicalSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sparse.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var returned uint32
	err = windows.DeviceIoControl(windows.Handle(f.Fd()), 0x900c4, nil, 0, nil, 0, &returned, nil)
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Truncate(64 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	info, err := probe(path)
	if err != nil || info.bytes >= 64<<20 || info.directory {
		t.Fatalf("allocated=%+v err=%v", info, err)
	}
}

func TestDirectoryMeasurementDoesNotBlockRename(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := openDirectory(source)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = os.Rename(source, target); err != nil {
		t.Fatalf("measurement holds a rename lock: %v", err)
	}
}
