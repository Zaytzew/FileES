package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/localrepo"
)

func TestAnchorFolderIconPreservesFramesAndBaseArtwork(t *testing.T) {
	base := append([]byte(nil), managedFolderIcon...)
	icon, err := anchorFolderIconBytes()
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := shelfFolderIconBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(icon, base) || bytes.Equal(icon, shelf) || !bytes.Equal(base, managedFolderIcon) {
		t.Fatal("anchor must be distinct, without mutating the embedded artwork")
	}
	n := int(binary.LittleEndian.Uint16(base[4:6]))
	if !bytes.Equal(icon[:6], base[:6]) {
		t.Fatal("ICO header changed")
	}
	for i := 0; i < n; i++ {
		e := 6 + 16*i
		if !bytes.Equal(icon[e:e+8], base[e:e+8]) {
			t.Fatal("frame dimensions changed")
		}
		o, size := int(binary.LittleEndian.Uint32(icon[e+12:])), int(binary.LittleEndian.Uint32(icon[e+8:]))
		bo, bs := int(binary.LittleEndian.Uint32(base[e+12:])), int(binary.LittleEndian.Uint32(base[e+8:]))
		img, err := png.Decode(bytes.NewReader(icon[o : o+size]))
		if err != nil {
			t.Fatal(err)
		}
		original, err := png.Decode(bytes.NewReader(base[bo : bo+bs]))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds() != original.Bounds() {
			t.Fatal("decoded dimensions changed")
		}
		changed := false
		for y := 0; y < img.Bounds().Dy(); y++ {
			for x := 0; x < img.Bounds().Dx(); x++ {
				r, g, b, a := img.At(x, y).RGBA()
				br, bg, bb, ba := original.At(x, y).RGBA()
				if r != br || g != bg || b != bb || a != ba {
					changed = true
					if x < img.Bounds().Dx()/2 || y < img.Bounds().Dy()/2 {
						t.Fatal("badge changed artwork outside lower-right corner")
					}
				}
			}
		}
		if !changed {
			t.Fatalf("frame %d has no badge", i)
		}
	}
}

func TestAnchorFolderIconIsStableAndSelectedFromLifecycle(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, anchor := range []bool{false, true} {
		attachments := make(chan provisionedAttachment, 1)
		p := daemonProvisioner{attachments: attachments}
		p.publishLocalRecord(context.Background(), localrepo.Record{Anchor: anchor}, clientprofile.Profile{})
		repo := (<-attachments).Repo
		if repo.Anchor != anchor {
			t.Fatal("lifecycle identity lost")
		}
		raw, err := json.Marshal(repo)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("Anchor")) || bytes.Contains(raw, []byte("anchor")) {
			t.Fatal("local identity leaked into configuration")
		}
		path, err := repositoryFolderIconPath(repo.Anchor)
		if err != nil {
			t.Fatal(err)
		}
		want := "filees-folder.ico"
		if anchor {
			want = "filees-anchor.ico"
		}
		if filepath.Base(path) != want {
			t.Fatalf("icon = %s", path)
		}
		stamp := time.Unix(1700000000, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := repositoryFolderIconPath(repo.Anchor); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(stamp) {
			t.Fatal("unchanged icon rewritten")
		}
	}
}
