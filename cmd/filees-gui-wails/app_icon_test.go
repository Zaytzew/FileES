package main

import (
	"bytes"
	"image"
	_ "image/png"
	"os"
	"strings"
	"testing"
)

// The taskbar button must have an icon, and nothing supplies one by accident.
//
// Wails looks for icon resource 3 inside the executable first, and a Go binary
// carries no icon resource unless one is generated and linked in; only then
// does it fall back to application.Options.Icon. With neither, the window
// appeared on the taskbar with no icon at all - present, blank, and no error
// anywhere to say why. That is the shape of fault this checks for: not a crash,
// an absence.
func TestTheApplicationCarriesAnIcon(t *testing.T) {
	if len(appIcon) == 0 {
		t.Fatal("no application icon is embedded; the taskbar button would be blank")
	}
	// Decoded rather than sniffed. A truncated or renamed file passes a
	// signature check and then fails inside Windows, where the only symptom is
	// the missing icon again.
	config, format, err := image.DecodeConfig(bytes.NewReader(appIcon))
	if err != nil {
		t.Fatalf("the embedded icon does not decode: %v", err)
	}
	if format != "png" {
		t.Fatalf("icon format = %q; Windows takes icon resource bytes here, and an .ico file is a container with a directory in front of the images", format)
	}
	// Windows asks for the system icon size, which is 32 or 48 logical pixels
	// and more on a scaled display. Anything small enough to be a tray glyph
	// would be visibly soft on the taskbar.
	if config.Width < 128 || config.Height < 128 {
		t.Errorf("icon is %dx%d; too small to scale cleanly to the taskbar", config.Width, config.Height)
	}
	if config.Width != config.Height {
		t.Errorf("icon is %dx%d; a non-square icon is letterboxed by Windows", config.Width, config.Height)
	}
}

// And it has to reach Wails. The embed alone changes nothing: the option is
// what the window code reads.
func TestTheIconIsHandedToTheApplication(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "application.New(application.Options{")
	if start < 0 {
		t.Fatal("the application is no longer constructed with application.Options")
	}
	end := strings.Index(source[start:], "\n\t})")
	if end < 0 {
		t.Fatal("could not find the end of the application options")
	}
	if !strings.Contains(source[start:start+end], "Icon:") {
		t.Error("application.Options carries no Icon; the embedded icon would never reach the window")
	}
}

// The small icon has to be applied too, and after the application has started.
//
// Wails sets only ICON_BIG - windowsWebviewWindow.setIcon sends WM_SETICON with
// ICON_BIG and nothing else - so Alt-Tab and the title bar fall back to the
// default glyph. Measured after the first fix: WM_GETICON returned a handle for
// ICON_BIG and zero for ICON_SMALL, which is half a fix, and the missing half is
// the one somebody sees while holding Alt-Tab.
//
// The placement matters as much as the call: the native window handle does not
// exist until the application has started, so applying it beside the window's
// construction would compile, run, and do nothing.
func TestTheSmallWindowIconIsAppliedAfterStartup(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "applySmallWindowIcon(mainWindow, appIcon)") {
		t.Fatal("nothing applies the small window icon; Alt-Tab would show the default glyph")
	}
	started := strings.Index(source, "events.Common.ApplicationStarted")
	applied := strings.Index(source, "applySmallWindowIcon(mainWindow, appIcon)")
	if started < 0 || applied < started {
		t.Error("the small icon is applied before the application has started, when there is no native window handle yet")
	}
}
