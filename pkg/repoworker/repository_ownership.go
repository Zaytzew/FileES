package repoworker

import (
	"fmt"
	"os/user"
	"strconv"
)

// RepositoryOwnershipError names a repository file the worker does not own.
//
// spot, 2026-09-25: db/rep-cache.db and its journal belonged to root (an
// administrative svnadmin run on 22.08); svnadmin freeze needs to write them,
// so every deletion of that repository failed and was retried without end as
// DELETE_REPOSITORY_RETRY, with nothing saying which file or whose it was.
type RepositoryOwnershipError struct {
	Repo, Path   string // Path is relative to Repo
	UID, WantUID int
}

func (e *RepositoryOwnershipError) Error() string {
	return fmt.Sprintf("repository file %s is owned by %s, not by %s that runs the repository worker; the server installer corrects it, or: chown -R %s %s",
		e.Path, uidLabel(e.UID), uidLabel(e.WantUID), uidName(e.WantUID), e.Repo)
}

func uidName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

func uidLabel(uid int) string {
	if name := uidName(uid); name != strconv.Itoa(uid) {
		return fmt.Sprintf("%s (uid %d)", name, uid)
	}
	return fmt.Sprintf("uid %d", uid)
}
