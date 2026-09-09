//go:build windows && native_svn_bundle

package nativeruntime

import _ "embed"

// The packager replaces the tracked sentinel through a Go build overlay.
// A plain tagged build cannot accidentally publish the sentinel: startup and
// the release packager both validate the archive before using it.
//
//go:embed runtime.payload
var Payload []byte
