//go:build windows && native_cfapi_probe

package nativecfapiprobe

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLongPathHydrationPreservesLocalEdits(t *testing.T) {
	root := anchor(t)
	rel := ""
	for len(filepath.Join(root, rel, "drawing.dwg")) < 280 {
		rel = filepath.Join(rel, strings.Repeat("folder", 5))
	}
	if err := os.MkdirAll(filepath.Join(root, rel), 0700); err != nil {
		t.Fatal(err)
	}
	identity := filepath.ToSlash(filepath.Join(rel, "drawing.dwg"))
	content := "test drawing bytes"
	cache := filepath.Join(t.TempDir(), strings.Repeat("cache", 20), strings.Repeat("cache", 20), "bytes")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if got := run(t, "f\t"+strconv.Itoa(len(content))+"\t"+identity+"\tdrawing.dwg\n", "placeholders", "--root", root, "--rel", rel); !got.OK {
		t.Fatalf("seed: %+v", got)
	}
	provider := connectAnchor(t, root, func(request []string) string {
		if request[4] != identity {
			return "err\t" + request[1] + "\twrong identity"
		}
		return "ok\t" + request[1] + "\t" + cache
	})
	if got := run(t, "", "hydrate", "--root", root, "--rel", identity); !got.OK {
		t.Fatalf("hydrate: %+v", got)
	}
	file := filepath.Join(root, filepath.FromSlash(identity))
	if raw, err := os.ReadFile(file); err != nil || string(raw) != content {
		t.Fatalf("bytes=%q err=%v", raw, err)
	}
	drain(provider.requests)
	if err := os.WriteFile(file, []byte("local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := run(t, "", "hydrate", "--root", root, "--rel", identity); !got.OK {
		t.Fatalf("local hydrate: %+v", got)
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != "local edit" {
		t.Fatalf("local bytes=%q err=%v", raw, err)
	}
	select {
	case req := <-provider.requests:
		t.Fatalf("refetched local bytes: %v", req)
	default:
	}
}

func TestLongPathPlaceholderCanBeInspectedAndSeededAgain(t *testing.T) {
	root := anchor(t)
	rel := ""
	name := "rys-1-rzut-przestrzeni-instalacyjnej-A.tif"
	for len(filepath.Join(root, rel, name)) < 270 {
		rel = filepath.Join(rel, strings.Repeat("a", 24))
	}
	if err := os.MkdirAll(filepath.Join(root, rel), 0700); err != nil {
		t.Fatal(err)
	}
	identity := filepath.ToSlash(filepath.Join(rel, name))
	listing := "f\t7638518\t" + identity + "\t" + name + "\n"
	for attempt := 0; attempt < 2; attempt++ {
		if got := run(t, listing, "placeholders", "--root", root, "--rel", rel); !got.OK {
			t.Fatalf("seed/retry %d, %d characters: %+v", attempt, len(filepath.Join(root, rel, name)), got)
		}
		if got := run(t, "", "file-state", "--root", root, "--rel", identity); !got.OK || !got.Partial {
			t.Fatalf("metadata inspection: %+v", got)
		}
	}
}

func TestLongPathThroughSeededDirectories(t *testing.T) {
	root := anchor(t)
	parent := ""
	parts := []string{"2024", "SZPITAL-GRODZISK", "ROBOCZE-własne", "03-archiwalia-skanowanie-własne-i-od-szpitala", "Skany od szpitala - tiff", "Skany od szpitala", "Budynek A", "A_1", "rys 9 - rzut VI pietra A"}
	for _, name := range parts {
		identity := filepath.ToSlash(filepath.Join(parent, name))
		listing := "d\t0\t" + identity + "\t" + name + "\n"
		if got := run(t, listing, "placeholders", "--root", root, "--rel", parent); !got.OK {
			t.Fatalf("directory %s: %+v", identity, got)
		}
		parent = identity
	}
	name := "rys 9 - rzut VI pietra A.tif"
	identity := parent + "/" + name
	listing := "f\t10372934\t" + identity + "\t" + name + "\n"
	// The coordinator pins ancestors against replacement while seeding.
	var guards []windows.Handle
	defer func() {
		for _, h := range guards {
			windows.CloseHandle(h)
		}
	}()
	for p := filepath.Join(root, filepath.FromSlash(parent)); ; p = filepath.Dir(p) {
		wide, err := windows.UTF16PtrFromString(p)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.CreateFile(wide, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			t.Fatal(err)
		}
		guards = append(guards, h)
		if p == filepath.Dir(p) {
			break
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if got := run(t, listing, "placeholders", "--root", root, "--rel", parent); !got.OK {
			t.Fatalf("file %d, %d characters: %+v", attempt, len(filepath.Join(root, identity)), got)
		}
		if got := run(t, "", "file-state", "--root", root, "--rel", identity); !got.OK || !got.Partial {
			t.Fatalf("metadata inspection: %+v", got)
		}
	}
}
