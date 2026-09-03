package realmbranding

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
	"testing"
)

// bigPNG builds an image too large to travel as-is, with enough variation that
// PNG compression cannot shrink it back under the limit.
func bigPNG(t *testing.T, edge int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, edge, edge))
	for y := 0; y < edge; y++ {
		for x := 0; x < edge; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 13), B: uint8(x*y + 29), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if buf.Len() <= MaxLogoBytes {
		t.Fatalf("fixture is %d bytes and proves nothing under a %d limit", buf.Len(), MaxLogoBytes)
	}
	return buf.Bytes()
}

// An administrator putting a PNG in /etc/filees is doing the same thing as an
// owner picking one in the interface, and until 2026-09-03 the two paths
// disagreed: the interface scaled what it was given, the configuration only
// measured it. The same file was accepted from a client and refused from
// server.json, which is how the owner met it.
func TestTheConfigurationPathAcceptsWhatTheClientWouldPrepare(t *testing.T) {
	raw := bigPNG(t, 600)

	if _, err := FromBytes("#2D5A3D", "image/png", raw); err == nil {
		t.Fatal("this fixture must be too large for the measuring path, or the test proves nothing")
	}
	prepared, err := PrepareLogo("#2D5A3D", "image/png", raw)
	if err != nil {
		t.Fatalf("the preparing path must accept an ordinary image: %v", err)
	}
	if prepared.LogoBase64 == "" || prepared.LeadingColor != "#2D5A3D" {
		t.Fatalf("prepared = %+v", prepared)
	}
}

// An image already small enough passes through untouched, so nothing that
// worked before this change behaves differently.
func TestAnAlreadySmallLogoIsNotTouched(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	direct, err := FromBytes("#2D5A3D", "image/png", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareLogo("#2D5A3D", "image/png", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if prepared.LogoBase64 != direct.LogoBase64 {
		t.Fatal("an image within the limit must reach the record unchanged")
	}
}

// Three causes used to share one sentence - "logo content is invalid or too
// large" - which named no limit and left the reader unable to tell a corrupt
// file from an oversized one, or to know how much smaller it had to be.
func TestEachWayALogoIsRefusedSaysWhichOneItWas(t *testing.T) {
	if _, err := Normalize(Branding{LeadingColor: "#2D5A3D", LogoMediaType: "image/png", LogoBase64: "!!! nie base64 !!!"}); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("a corrupt encoding must say so: %v", err)
	}
	if _, err := Normalize(Branding{LeadingColor: "#2D5A3D", LogoMediaType: "image/png", LogoBase64: "  "}); err == nil {
		t.Fatal("an empty logo must be refused")
	}

	oversized := bigPNG(t, 600)
	_, err := FromBytes("#2D5A3D", "image/png", oversized)
	if err == nil {
		t.Fatal("an oversized logo must be refused by the measuring path")
	}
	// The size and the limit are the two facts that make this actionable.
	if !strings.Contains(err.Error(), strconv.Itoa(len(oversized))) || !strings.Contains(err.Error(), strconv.Itoa(MaxLogoBytes)) {
		t.Fatalf("the message names neither the size nor the limit: %v", err)
	}
	if !strings.Contains(err.Error(), "client") {
		t.Fatalf("the message should point at the path that would have fitted it: %v", err)
	}
}
