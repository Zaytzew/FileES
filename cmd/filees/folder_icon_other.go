//go:build !windows

package main

// markManagedFolder and unmarkManagedFolder are no-ops outside Windows.
//
// The mechanism they use - a desktop.ini read by Explorer - is specific to the
// Windows shell and has no counterpart worth imitating elsewhere. GNOME reads
// a per-directory metadata store owned by the file manager, macOS uses a
// resource fork on a hidden Icon file; both are the file manager's business
// rather than a daemon's, and neither is asked for.
func markManagedFolder(root, iconPath string) error { return nil }

func unmarkManagedFolder(root string) error { return nil }
