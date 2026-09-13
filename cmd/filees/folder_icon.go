package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"filees/pkg/clientprofile"
)

// The folder icon shown on every working copy FileES keeps.
//
// Embedded rather than installed beside the binary because Explorer stores the
// icon's path, not the icon: a file that moved or vanished with the next client
// update would leave every managed folder rendering as a blank rectangle. The
// daemon writes it once into its own state directory, which no installer
// touches, and points every folder at that one copy.
//
//go:embed assets/filees-folder.ico
var managedFolderIcon []byte

// managedFolderIconPath returns the on-disk icon, writing it if needed.
//
// The write is content-checked rather than unconditional. This runs on every
// repository start, and rewriting the file each time would invalidate the
// shell's icon cache for every managed folder on every daemon restart.
func managedFolderIconPath() (string, error) {
	dir := filepath.Dir(clientprofile.DefaultRoot())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "filees-folder.ico")
	if existing, err := os.ReadFile(path); err == nil && len(existing) == len(managedFolderIcon) {
		return path, nil
	}
	if err := os.WriteFile(path, managedFolderIcon, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Shelf decoration reuses the official folder artwork with the same violet
// purpose dot used by the desktop. Every embedded ICO size retains its artwork.
func shelfFolderIconPath() (string, error) {
	base, err := managedFolderIconPath()
	if err != nil {
		return "", err
	}
	data, err := shelfFolderIconBytes()
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(base), "filees-shelf.ico")
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return path, nil
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func shelfFolderIconBytes() ([]byte, error) {
	n := int(binary.LittleEndian.Uint16(managedFolderIcon[4:6]))
	header := append([]byte(nil), managedFolderIcon[:6+16*n]...)
	result := append([]byte(nil), header...)
	for i := 0; i < n; i++ {
		entry := 6 + i*16
		size := int(binary.LittleEndian.Uint32(header[entry+8:]))
		offset := int(binary.LittleEndian.Uint32(header[entry+12:]))
		if offset < 0 || size < 0 || offset+size > len(managedFolderIcon) {
			return nil, fmt.Errorf("invalid embedded ICO")
		}
		img, err := png.Decode(bytes.NewReader(managedFolderIcon[offset : offset+size]))
		if err != nil {
			return nil, err
		}
		b := img.Bounds()
		canvas := image.NewNRGBA(b)
		draw.Draw(canvas, b, img, b.Min, draw.Src)
		radius := b.Dx() / 6
		cx, cy := b.Max.X-radius-1, b.Max.Y-radius-1
		for y := cy - radius; y <= cy+radius; y++ {
			for x := cx - radius; x <= cx+radius; x++ {
				if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= radius*radius {
					canvas.Set(x, y, color.NRGBA{R: 115, G: 101, B: 207, A: 255})
				}
			}
		}
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, canvas); err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint32(result[entry+8:], uint32(encoded.Len()))
		binary.LittleEndian.PutUint32(result[entry+12:], uint32(len(result)))
		result = append(result, encoded.Bytes()...)
	}
	return result, nil
}
