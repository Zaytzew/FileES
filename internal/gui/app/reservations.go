package app

import (
	"crypto/sha256"
	"encoding/hex"

	contract "filees/pkg/contract/v1"
)

// Reservation is the GUI's display and release model for a server-scoped SVN
// lock. It intentionally mirrors only the public IPC data the GUI needs.
type Reservation struct {
	ID, ServerID, RepoID, WorkingCopy, Path string
	Token                                   string
	OwnerLabel, CreatedAt                   string
	CanRelease, LocalChanges                bool
	ActivePassport                          bool
}

// projectReservation retains the fencing token only inside the Go
// presentation model. ID is safe to hand to a renderer: it selects exactly
// this observed lock generation without revealing the token itself.
func projectReservation(serverID string, reservation contract.Reservation) Reservation {
	digest := sha256.Sum256([]byte(serverID + "\x00" + reservation.RepoID + "\x00" + reservation.WorkingCopy + "\x00" + reservation.Path + "\x00" + reservation.Token))
	return Reservation{
		ID: hex.EncodeToString(digest[:16]), ServerID: serverID,
		RepoID: reservation.RepoID, WorkingCopy: reservation.WorkingCopy,
		Path: reservation.Path, Token: reservation.Token,
		OwnerLabel: reservation.OwnerLabel, CreatedAt: reservation.CreatedAt,
		CanRelease: reservation.CanRelease, LocalChanges: reservation.LocalChanges,
		ActivePassport: reservation.ActivePassport,
	}
}

// ReservationReleaseRequest is forwarded by the composition root to IPC. The
// token fences a selection made in a potentially stale native dialog.
type ReservationReleaseRequest struct {
	ServerID, RepoID, Path, ExpectedToken string
	ConfirmRisk                           bool
}
