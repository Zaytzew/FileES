// Package fsdurable holds the one portable answer to "flush this directory".
//
// pkg/deploy worked it out first and wrote the reasoning down; pkg/activation
// did not, and its twelve tests failed on Windows with "sync <dir>: Access is
// denied" - which was read as antivirus interference for long enough to reach
// a known-failure list. Measured 2026-09-08: the failure survived running
// inside an antivirus exclusion, so it was never that.
//
// One copy, so the next caller inherits the answer instead of the bug.
package fsdurable
