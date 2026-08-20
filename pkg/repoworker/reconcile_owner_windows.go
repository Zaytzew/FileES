package repoworker

// The canonical server is Unix/OpenBSD. Windows only runs repository-worker
// fixtures, where POSIX uid ownership does not exist.
func verifyServiceWorkingCopyOwner(string) error { return nil }
