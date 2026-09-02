// Package filees exposes build metadata shared by the desktop entry points.
package filees

import (
	_ "embed"
	"strings"
)

// embeddedVersion is the single source version file shipped with release
// bundles. Keeping the fallback in the binary also gives native Wails builds
// the right version when they are built directly, outside the legacy GUI
// packaging script.
//
//go:embed VERSION
var embeddedVersion string

// Version returns the normalized product version stored in VERSION.
func Version() string {
	return strings.TrimSpace(embeddedVersion)
}
