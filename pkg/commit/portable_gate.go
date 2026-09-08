package commit

import (
	"context"
	"os"
	"path"
	"path/filepath"

	"filees/pkg/filepolicy"
	"filees/pkg/portablepath"
	"filees/pkg/watcher"
)

// UnportableName is one object FileES declines to take under control because
// its name cannot exist, unchanged, on every platform this repository is used
// from.
type UnportableName struct {
	Rel    string
	Reason string
}

// gateProblem reports why rel cannot be taken under control, or nil when it
// can. It answers about the name alone plus its siblings on disk; whether the
// object is already versioned is the caller's question.
//
// Windows is deliberately expected to see almost nothing here. It cannot be the
// source of these names - the OS refuses CON.txt, a:b.txt and a second file
// differing only in case before any watcher sees them - so the gate does its
// work on the case-sensitive clients, which is exactly where the names come
// from. See concepts/PORTABLE_PATH_GATE_CONCEPT.md §2.
func gateProblem(wc, rel string) *portablepath.Problem {
	base := path.Base(rel)
	if problem := portablepath.SegmentProblem(base); problem != nil {
		return problem
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	entries, err := os.ReadDir(filepath.Join(wc, filepath.FromSlash(dir)))
	if err != nil {
		// Not being able to look is not evidence of a problem. Refusing here
		// would turn an unreadable directory into a permanent block on every
		// file inside it.
		return nil
	}
	siblings := make([]string, 0, len(entries))
	for _, entry := range entries {
		siblings = append(siblings, entry.Name())
	}
	if other := portablepath.Collides(base, siblings); other != "" {
		return &portablepath.Problem{Kind: portablepath.CaseCollision, Detail: other}
	}
	return nil
}

// refuseUnportable reports whether this event must not be staged, logging the
// reason once per event.
//
// Only new objects are gated. A path already under version control is past this
// question - refusing its modifications would strand work that FileES itself
// accepted earlier, and the name is by then somebody else's to change.
func (s *Service) refuseUnportable(ev watcher.Event) bool {
	if ev.Op != watcher.Added && ev.Op != watcher.Renamed {
		return false
	}
	if s.wc == "" {
		return false
	}
	problem := gateProblem(s.wc, ev.Rel)
	if problem == nil {
		return false
	}
	s.Logger.Warnf("nie obejmuję kontrolą %s: %s", ev.Rel, problem)
	return true
}

// UnportableNames lists everything in the working copy that FileES is declining
// to take under control, derived from what is on disk right now.
//
// It is derived rather than remembered on purpose. A remembered set would have
// to survive restarts and be cleared by whoever fixes the name, and a set that
// drifts from the disk is how a projection ends up telling the owner something
// that stopped being true - which this product has already paid for three
// times. Recomputing costs one svn status and cannot be wrong.
func (s *Service) UnportableNames(ctx context.Context, wc string) ([]UnportableName, error) {
	status, err := s.Cli.Status(ctx, wc, nil)
	if err != nil {
		return nil, err
	}
	var out []UnportableName
	for _, entry := range status {
		if entry.Item != "unversioned" {
			continue
		}
		rel := filepath.ToSlash(entry.Path)
		if filepolicy.IsBuiltinIgnored(rel) {
			continue
		}
		if problem := gateProblem(wc, rel); problem != nil {
			out = append(out, UnportableName{Rel: rel, Reason: problem.String()})
		}
	}
	return out, nil
}
