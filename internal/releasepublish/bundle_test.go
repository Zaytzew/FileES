package releasepublish

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBundleDirectoryIsDeterministicNormalizedAndSorted(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{"z.txt": "z", "bin/filees": "binary", "a.txt": "a"} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if path == "bin/filees" {
			mode = 0o711
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	first := filepath.Join(t.TempDir(), "first.tar.gz")
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err := BundleDirectory(root, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "z.txt"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := BundleDirectory(root, second); err != nil {
		t.Fatal(err)
	}
	one, _ := os.ReadFile(first)
	two, _ := os.ReadFile(second)
	if !reflect.DeepEqual(one, two) {
		t.Fatal("identical content produced different bundle bytes")
	}
	entries := readBundleHeaders(t, first)
	wanted := []string{"a.txt", "bin/", "bin/filees", "z.txt"}
	if !reflect.DeepEqual(entries.names, wanted) {
		t.Fatalf("bundle order = %v, want %v", entries.names, wanted)
	}
	if entries.modes["bin/filees"] != 0o755 || entries.modes["z.txt"] != 0o644 {
		t.Fatalf("bundle modes = %+v", entries.modes)
	}
	for _, header := range entries.headers {
		if header.Uid != 0 || header.Gid != 0 || !header.ModTime.Equal(time.Unix(0, 0)) {
			t.Fatalf("non-deterministic header = %+v", header)
		}
	}
}

func TestBundleDirectoryRejectsSymlinkAndEmptySource(t *testing.T) {
	empty := t.TempDir()
	if err := BundleDirectory(empty, filepath.Join(t.TempDir(), "empty.tar.gz")); err == nil {
		t.Fatal("empty source accepted")
	}
	root := t.TempDir()
	if err := os.Symlink("outside", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := BundleDirectory(root, filepath.Join(t.TempDir(), "link.tar.gz")); err == nil {
		t.Fatal("symlink source accepted")
	}
}

type bundleHeaders struct {
	names   []string
	modes   map[string]int64
	headers []*tar.Header
}

func readBundleHeaders(t *testing.T, path string) bundleHeaders {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	result := bundleHeaders{modes: make(map[string]int64)}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		copy := *header
		result.names = append(result.names, header.Name)
		result.modes[header.Name] = header.Mode
		result.headers = append(result.headers, &copy)
	}
	return result
}
