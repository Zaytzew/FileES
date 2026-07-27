package app

// Reservation is the GUI's display and release model for a server-scoped SVN
// lock. It intentionally mirrors only the public IPC data the GUI needs.
type Reservation struct {
	RepoID, WorkingCopy, Path, Token string
	Owner, CreatedAt                 string
	CanRelease, LocalChanges         bool
	ActivePassport                   bool
}

// ReservationReleaseRequest is forwarded by the composition root to IPC. The
// token fences a selection made in a potentially stale native dialog.
type ReservationReleaseRequest struct {
	ServerID, RepoID, Path, ExpectedToken string
	ConfirmRisk                           bool
}
