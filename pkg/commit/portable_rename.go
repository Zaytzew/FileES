package commit

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"filees/pkg/portablepath"
)

// Errors from RenameUnportable. They are distinct because the interface must
// tell them apart: a name that is still impossible is the person's to fix now,
// while a file the system will not let go of is a wait, not a mistake. Showing
// one as the other asks somebody to correct something they did right.
var (
	// ErrRenameNameUnportable - the proposed name has the same problem, or a
	// new one.
	ErrRenameNameUnportable = errors.New("proponowana nazwa też jest nieprzedstawialna")
	// ErrRenameTargetExists - something already occupies the new name.
	ErrRenameTargetExists = errors.New("obiekt o tej nazwie już istnieje")
	// ErrRenameBlockedInUse - the operating system refused, which on Windows
	// almost always means an application still holds the file open.
	ErrRenameBlockedInUse = errors.New("system nie pozwala teraz zmienić nazwy tego pliku")
	// ErrRenameNotRefused - the path is not one FileES is declining, so there
	// is nothing here to fix.
	ErrRenameNotRefused = errors.New("ta ścieżka nie jest wstrzymana przez bramkę nazw")
)

// RenameUnportable renames an object FileES is declining to take under control.
//
// It is deliberately a plain filesystem rename, not an svn one: the object was
// never added, so Subversion knows nothing about it and must not be asked to.
// The gate only ever refuses new objects, which is what makes this safe.
//
// The new name is checked before the rename rather than after. Renaming first
// and reporting afterwards would leave the person with two problems instead of
// one, and the check is the same one that refused the original name - a single
// rule, asked twice, so the answer cannot disagree with itself.
func (s *Service) RenameUnportable(wc, rel, newName string) error {
	if gateProblem(wc, rel) == nil {
		return ErrRenameNotRefused
	}
	if problem := portablepath.SegmentProblem(newName); problem != nil {
		return ErrRenameNameUnportable
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	absDir := filepath.Join(wc, filepath.FromSlash(dir))
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}
	siblings := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == path.Base(rel) {
			// The object being renamed is not its own obstacle.
			continue
		}
		siblings = append(siblings, entry.Name())
	}
	if portablepath.Collides(newName, siblings) != "" {
		return ErrRenameTargetExists
	}
	target := filepath.Join(absDir, newName)
	if _, err := os.Stat(target); err == nil {
		return ErrRenameTargetExists
	}
	if err := os.Rename(filepath.Join(absDir, path.Base(rel)), target); err != nil {
		if blockedByOpenFile(err) {
			return ErrRenameBlockedInUse
		}
		return err
	}
	s.Logger.Infof("bramka nazw: %s przemianowane na %s", rel, newName)
	// The list is derived, so it corrects itself on the next sweep. Waking it
	// now is what makes the condition disappear while the person is still
	// looking at it.
	s.wakeUnportableSweep()
	return nil
}

// blockedByOpenFile reports whether the operating system refused because
// something else holds the file, rather than because the request was wrong.
//
// Windows returns a sharing violation or a plain access denial for a file an
// application still has open, and both arrive here as fs.ErrPermission. That is
// broader than "open elsewhere" - a genuinely read-only directory lands in the
// same bucket - but the two cannot be told apart from the error alone, and the
// message for the wider case is still true: not now, try again.
func blockedByOpenFile(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}
