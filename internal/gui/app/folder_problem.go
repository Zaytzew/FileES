package app

import "strings"

// FolderProblemBorrowPending: local changes wait for an edit passport the
// server does not issue. Owner's production, 2026-09-25: the folder read
// "wymaga uwagi" for hours while the journal repeated LOCK-2104, and nothing
// on screen said what was waiting or what to do about it.
const FolderProblemBorrowPending = "borrow_pending"

// FolderProblem names why a folder needs attention in terms its user can act
// on. It is derived from live state only: passport issues vanish from the
// snapshot on recovery, so the problem does too.
type FolderProblem struct {
	Kind  string
	Path  string // the first waiting file, as the daemon reports it
	More  int    // further waiting files
	Since string
	// The most recent reason recorded for this folder's passports, when there
	// is one: its code and, once the domain catalogue rendered it, the
	// sentence. Older reasons are not repeated; the journal keeps them.
	Code   string
	Reason string
	// Holder names whoever else has the waiting file reserved, when the
	// reservation list shows it: the actual reason the passport is refused
	// (KRAŃCOWA-PŁOŃSK, 2026-09-25 - the card said "owner unknown").
	Holder, HolderSince string
}

// FolderProblem reports the problem of one repository, if it has one this
// function knows how to explain.
func (vm ViewModel) FolderProblem(repoID string) (FolderProblem, bool) {
	var problem FolderProblem
	var reasonAt string
	waiting := 0
	for _, entry := range vm.Errors {
		if entry.RepoID != repoID {
			continue
		}
		if strings.HasPrefix(entry.ID, "passport:") {
			// One entry per waiting passport (reducer.go rebuilds them from
			// the live snapshot on every projection).
			if waiting == 0 || entry.Timestamp < problem.Since {
				problem.Path, problem.Since = entry.MessageDetail, entry.Timestamp
			}
			waiting++
			continue
		}
		if strings.HasPrefix(entry.MessageKey, "passport.") && entry.Timestamp >= reasonAt {
			reasonAt = entry.Timestamp
			problem.Code = entry.Code
			problem.Reason = ""
			if entry.Message != "" && entry.Message != entry.MessageKey {
				problem.Reason = entry.Message
			}
		}
	}
	if waiting == 0 {
		return FolderProblem{}, false
	}
	problem.Kind = FolderProblemBorrowPending
	problem.More = waiting - 1
	want := strings.TrimPrefix(strings.ReplaceAll(problem.Path, "\\", "/"), "/")
	for _, reservation := range vm.Reservations {
		if reservation.RepoID == repoID && !reservation.CanRelease && strings.TrimPrefix(strings.ReplaceAll(reservation.Path, "\\", "/"), "/") == want {
			problem.Holder, problem.HolderSince = reservation.OwnerLabel, reservation.CreatedAt
			break
		}
	}
	return problem, true
}
