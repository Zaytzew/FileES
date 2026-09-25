package repoworker

// CheckRepositoryOwnership: see repository_ownership_unix.go. The server does
// not run on Windows.
func CheckRepositoryOwnership(string) error { return nil }
