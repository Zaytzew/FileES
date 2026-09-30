package mobileworker

import (
	"context"
	"errors"
	"os"
	"path"
)

// Validate before creating a working copy. SVN update must use this SAME
// revision, so a concurrent HEAD change cannot enlarge the measured download.
// Old contents/pristines and incoming bytes have separate 2 GiB budgets.
// This bounds payload bytes, not SVN properties, WC metadata or concurrency.
func (s SVNAppender) checkTreeBudget(ctx context.Context, repo, parent string, files []TreeFile) (int64, error) {
	if len(files) == 0 || len(files) > maxTreeFiles {
		return 0, errUploadLimit
	}
	rev := files[0].BaseRevision
	if rev < 0 {
		return 0, errors.New("invalid mobile write base revision")
	}
	reader := SVNReader{SvnPath: s.SvnPath, SvnlookPath: s.SvnlookPath}
	var incoming, existing int64
	for _, file := range files {
		if file.BaseRevision != rev {
			return 0, errors.New("mixed mobile write base revisions")
		}
		info, err := os.Stat(file.SpoolPath)
		if err != nil {
			return 0, err
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxTreeUncompressed-incoming {
			return 0, errUploadLimit
		}
		incoming += info.Size()
		if file.Replace {
			size, err := reader.FileSize(ctx, repo, path.Join(parent, file.RelPath), rev)
			if err != nil {
				return 0, err
			}
			if size < 0 || size > maxTreeUncompressed-existing {
				return 0, errUploadLimit
			}
			existing += size
		}
	}
	return rev, nil
}
