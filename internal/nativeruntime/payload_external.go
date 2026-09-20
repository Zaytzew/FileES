//go:build (!windows && !linux) || !native_svn_bundle

package nativeruntime

// Untagged developer builds and other platforms keep explicit helper selection.
var Payload []byte
