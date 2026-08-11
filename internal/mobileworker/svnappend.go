package mobileworker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"filees/pkg/passport"
)

// SVNAppender publishes a new file with one controlled commit and no persistent
// working copy. It uses a transient depth-empty checkout of the parent, which —
// unlike svn import — lets it set svn:needs-lock atomically in the same commit.
// (svnmucc would be the leaner tool where available; this path needs no extra
// binary.)
type SVNAppender struct {
	SvnPath     string
	SvnlookPath string
}

func (s SVNAppender) svn() string {
	if s.SvnPath != "" {
		return s.SvnPath
	}
	return "svn"
}

func (s SVNAppender) svnlook() string {
	if s.SvnlookPath != "" {
		return s.SvnlookPath
	}
	return "svnlook"
}

var revLine = regexp.MustCompile(`([0-9]+)`)

// AppendFile checks out the parent at depth empty into a scratch WC, drops the
// spooled file in, sets svn:needs-lock, and commits with the request-id revprop.
// It returns the committed revision. A commit against an existing target fails,
// which the worker resolves as a name collision.
func (s SVNAppender) AppendFile(ctx context.Context, repoPath, parentPath, filename, spoolPath, requestID string) (int64, error) {
	wc, err := os.MkdirTemp("", "filees-mobile-wc-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(wc)

	coURL := fileURL(repoPath)
	if parentPath != "" {
		coURL += "/" + parentPath
	}
	if err := runStream(ctx, io.Discard, s.svn(), "checkout", "-q", "--depth", "empty", coURL, wc); err != nil {
		return 0, err
	}

	fileInWC := filepath.Join(wc, filename)
	if err := copyFile(spoolPath, fileInWC); err != nil {
		return 0, err
	}
	if err := runStream(ctx, io.Discard, s.svn(), "add", "-q", fileInWC); err != nil {
		return 0, err
	}
	if err := runStream(ctx, io.Discard, s.svn(), "propset", "-q", "svn:needs-lock", "*", fileInWC); err != nil {
		return 0, err
	}
	// The mobile channel is append-only, so its svn:needs-lock states a
	// different intent from the repository editing policy: these files are not
	// meant to be edited at all, by anyone, whether or not the repository uses
	// edit passports. Marking that intent explicitly is what lets the policy's
	// rollback (passport.ClearNeedsLock) leave them alone instead of reading
	// the bare property as something it had set itself.
	if err := runStream(ctx, io.Discard, s.svn(), "propset", "-q", passport.AppendOnlyProperty, "*", fileInWC); err != nil {
		return 0, err
	}
	if err := runStream(ctx, io.Discard, s.svn(), "commit", wc, "-m", "mobile append", "--with-revprop", "filees:request-id="+requestID); err != nil {
		return 0, err
	}

	// The committed revision is HEAD after our commit. A per-repo worker lock
	// makes this exact; the revprop remains the recovery anchor either way.
	out, err := output(ctx, s.svnlook(), "youngest", repoPath)
	if err != nil {
		return 0, err
	}
	m := revLine.FindString(string(out))
	rev, err := strconv.ParseInt(m, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("svnlook youngest: unexpected output %q", out)
	}
	return rev, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
