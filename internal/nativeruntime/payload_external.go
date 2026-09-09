//go:build !windows || !native_svn_bundle

package nativeruntime

// Developer builds and non-Windows releases keep explicit helper selection.
var Payload []byte
