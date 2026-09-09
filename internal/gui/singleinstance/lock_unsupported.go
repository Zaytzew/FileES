//go:build !linux && !windows

package singleinstance

import "errors"

// ErrUnsupportedPlatform reports that this platform has no desktop GUI, and
// therefore no per-user GUI lock to take.
var ErrUnsupportedPlatform = errors.New("single-instance locking is unsupported on this platform")

// Acquire refuses, and this file exists so that the refusal is a decision
// rather than a compiler error.
//
// FileES desktop targets are Windows and Linux. OpenBSD is the server and is
// not going to be a desktop — a person running OpenBSD reaches for plain SVN
// long before they reach for a GUI. That is a product decision, and until
// 2026-09-09 the only trace of it anywhere was `GOOS=openbsd go build ./...`
// stopping with "undefined: singleinstance.Acquire". An unwritten decision
// and an oversight are indistinguishable from the outside, and this one was
// read as the second.
//
// The cost of leaving it that way was concrete rather than aesthetic: a
// cross-build check of the SERVER tree could not complete, because it tripped
// over a DESKTOP package it had no interest in. Verifying a server-side change
// against all three targets is exactly when that check earns its keep.
//
// So the tree builds everywhere, no GUI is shipped where none belongs, and
// anyone who does start one on such a platform is told why instead of being
// handed a lock that pretends to work.
func Acquire(string) (Lock, error) {
	return nil, ErrUnsupportedPlatform
}
