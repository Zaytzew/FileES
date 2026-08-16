package mobileworker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	v1 "filees/pkg/mobile/v1"
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

// AppendFile checks out the repository root at depth empty, creates any
// missing parent directories in the same commit (mobile-uploads/…), drops
// the spooled file in, sets svn:needs-lock, and commits with the request-id
// revprop. A commit against an existing target fails, which the worker
// resolves as a name collision.
func (s SVNAppender) AppendFile(ctx context.Context, repoPath, parentPath, filename, spoolPath, requestID string) (int64, error) {
	wc, err := os.MkdirTemp("", "filees-mobile-wc-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(wc)

	if err := runStream(ctx, io.Discard, s.svn(), "checkout", "-q", "--depth", "empty", fileURL(repoPath), wc); err != nil {
		return 0, err
	}
	if err := s.ensureParent(ctx, wc, repoPath, parentPath); err != nil {
		return 0, err
	}
	destDir := wc
	if parentPath != "" {
		destDir = filepath.Join(append([]string{wc}, strings.Split(parentPath, "/")...)...)
	}
	fileInWC := filepath.Join(destDir, filename)
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

// CommitTree checks out the repo root at depth empty and publishes every
// extracted file in one commit: mkdir missing parents, add new objects
// (with svn:needs-lock + append-only), overwrite existing ones.
func (s SVNAppender) CommitTree(ctx context.Context, repoPath, parentPath string, files []TreeFile, requestID string) (int64, error) {
	if len(files) == 0 {
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
	wc, err := os.MkdirTemp("", "filees-mobile-tree-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(wc)
	if err := runStream(ctx, io.Discard, s.svn(), "checkout", "-q", "--depth", "empty", fileURL(repoPath), wc); err != nil {
		return 0, err
	}
	for _, file := range files {
		rel := strings.Trim(file.RelPath, "/")
		fullRel := rel
		if parentPath != "" {
			fullRel = strings.Trim(parentPath, "/") + "/" + rel
		}
		parent := ""
		if i := strings.LastIndex(fullRel, "/"); i >= 0 {
			parent = fullRel[:i]
		}
		if err := s.ensureParent(ctx, wc, repoPath, parent); err != nil {
			return 0, err
		}
		fileInWC := filepath.Join(append([]string{wc}, strings.Split(fullRel, "/")...)...)
		if file.Replace {
			if err := runStream(ctx, io.Discard, s.svn(), "update", "-q", "--set-depth", "empty", fileInWC); err != nil {
				return 0, err
			}
		}
		if err := copyFile(file.SpoolPath, fileInWC); err != nil {
			return 0, err
		}
		if !file.Replace {
			if err := runStream(ctx, io.Discard, s.svn(), "add", "-q", fileInWC); err != nil {
				return 0, err
			}
			if err := runStream(ctx, io.Discard, s.svn(), "propset", "-q", "svn:needs-lock", "*", fileInWC); err != nil {
				return 0, err
			}
			if err := runStream(ctx, io.Discard, s.svn(), "propset", "-q", passport.AppendOnlyProperty, "*", fileInWC); err != nil {
				return 0, err
			}
		}
	}
	if err := runStream(ctx, io.Discard, s.svn(), "commit", wc, "-m", "mobile tree ingest", "--with-revprop", "filees:request-id="+requestID); err != nil {
		return 0, err
	}
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

// ensureParent brings each parent segment into the sparse WC: update if it
// already exists in the repository, otherwise svn mkdir. The new directories
// are committed together with the uploaded file.
func (s SVNAppender) ensureParent(ctx context.Context, wc, repoPath, parentPath string) error {
	if parentPath == "" {
		return nil
	}
	reader := SVNReader{SvnPath: s.SvnPath, SvnlookPath: s.SvnlookPath}
	rev, err := reader.Youngest(ctx, repoPath)
	if err != nil {
		return err
	}
	var rel string
	for _, seg := range strings.Split(parentPath, "/") {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		if rel == "" {
			rel = seg
		} else {
			rel += "/" + seg
		}
		abs := filepath.Join(wc, filepath.FromSlash(rel))
		if info, err := os.Stat(abs); err == nil {
			if !info.IsDir() {
				return fmt.Errorf("parent path %q is not a directory", rel)
			}
			continue
		}
		kind, exists, err := reader.Stat(ctx, repoPath, rel, rev)
		if err != nil {
			return err
		}
		if exists {
			if kind != v1.KindDirectory {
				return fmt.Errorf("parent path %q is not a directory", rel)
			}
			if err := runStream(ctx, io.Discard, s.svn(), "update", "-q", "--set-depth", "empty", abs); err != nil {
				return err
			}
			continue
		}
		if err := runStream(ctx, io.Discard, s.svn(), "mkdir", "-q", abs); err != nil {
			return err
		}
	}
	return nil
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
