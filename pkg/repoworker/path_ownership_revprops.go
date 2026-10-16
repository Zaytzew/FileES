package repoworker

import (
	"context"
	"errors"
	"strconv"

	"filees/pkg/pathownership"
)

// One bounded local SVN read, without changed-path lists or document content.
// A complete log is required: omitted/malformed revisions are not unchanged.
func (s SVNPathOwners) refreshHistoryAuthors(ctx context.Context, target string, cached pathownership.Snapshot) (pathownership.Snapshot, bool, error) {
	var revisions []pathownership.Revision
	if cached.Revision > 0 {
		rev := strconv.FormatInt(cached.Revision, 10)
		raw, err := s.run(ctx, "log", "--xml", "--quiet", "-r", "1:"+rev, target+"@"+rev)
		if err != nil {
			return pathownership.Snapshot{}, false, err
		}
		revisions, err = pathownership.ParseLog(raw)
		if err != nil {
			return pathownership.Snapshot{}, false, err
		}
	}
	return rebindHistoryAuthors(ctx, cached, revisions)
}

func rebindHistoryAuthors(ctx context.Context, cached pathownership.Snapshot, revisions []pathownership.Revision) (pathownership.Snapshot, bool, error) {
	if cached.Revision < 0 || int64(len(revisions)) != cached.Revision {
		return pathownership.Snapshot{}, false, errors.New("incomplete ownership revision properties")
	}
	authors := make(map[int64]string, len(revisions))
	for _, rev := range revisions {
		if err := ctx.Err(); err != nil {
			return pathownership.Snapshot{}, false, err
		}
		if _, duplicate := authors[rev.Number]; duplicate || rev.Number < 1 || rev.Number > cached.Revision {
			return pathownership.Snapshot{}, false, errors.New("invalid ownership revision properties")
		}
		authors[rev.Number] = rev.Author
	}
	next := cached
	next.Entries = append([]pathownership.Entry(nil), cached.Entries...)
	changed := false
	for i := range next.Entries {
		if err := ctx.Err(); err != nil {
			return pathownership.Snapshot{}, false, err
		}
		entry := &next.Entries[i]
		author, ok := authors[entry.CreatedRevision]
		if !ok {
			return pathownership.Snapshot{}, false, errors.New("ownership origin revision unavailable")
		}
		if entry.FirstCommitter != author {
			entry.FirstCommitter = author
			changed = true
		}
		entry.OwnerRealmID = "" // operational authority is resolved anew, not cached
	}
	return next, changed, nil
}
