// Package mobileworker implements the server-side FileES mobile workers. The
// browser worker is read-only: it reads a repository without a working copy
// (svnlook / svn list) and never modifies state.
package mobileworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	v1 "filees/pkg/mobile/v1"
)

// SVNReader reads a repository server-side without a working copy. It shells out
// to svn/svnlook; both are read-only verbs against the repository database.
type SVNReader struct {
	SvnPath     string // default "svn"
	SvnlookPath string // default "svnlook"
}

func (r SVNReader) svn() string {
	if r.SvnPath != "" {
		return r.SvnPath
	}
	return "svn"
}

func (r SVNReader) svnlook() string {
	if r.SvnlookPath != "" {
		return r.SvnlookPath
	}
	return "svnlook"
}

// Youngest returns the repository HEAD revision.
func (r SVNReader) Youngest(ctx context.Context, repoPath string) (int64, error) {
	out, err := output(ctx, r.svnlook(), "youngest", repoPath)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("svnlook youngest: unexpected output %q", strings.TrimSpace(string(out)))
	}
	return n, nil
}

type xmlLists struct {
	List struct {
		Entries []struct {
			Kind   string `xml:"kind,attr"`
			Name   string `xml:"name"`
			Size   *int64 `xml:"size"`
			Commit struct {
				Revision int64 `xml:"revision,attr"`
			} `xml:"commit"`
		} `xml:"entry"`
	} `xml:"list"`
}

// List builds the manifest entries for one revision in a single recursive call.
// content_hash is left nil: svn list gives no SHA-256, and hashing every path
// would turn a listing into a backup (computed on demand at collision instead).
func (r SVNReader) List(ctx context.Context, repoPath string, rev int64) ([]v1.ManifestEntry, error) {
	out, err := output(ctx, r.svn(), "list", "--xml", "-R", "-r", strconv.FormatInt(rev, 10), fileURL(repoPath))
	if err != nil {
		return nil, err
	}
	var lists xmlLists
	if err := xml.Unmarshal(out, &lists); err != nil {
		return nil, fmt.Errorf("parse svn list xml: %w", err)
	}
	entries := make([]v1.ManifestEntry, 0, len(lists.List.Entries))
	for _, e := range lists.List.Entries {
		kind := v1.KindFile
		if e.Kind == "dir" {
			kind = v1.KindDirectory
		}
		entry := v1.ManifestEntry{
			Path:                e.Name,
			Kind:                kind,
			LastChangedRevision: e.Commit.Revision,
			ContentHash:         nil,
		}
		if e.Size != nil {
			entry.Size = *e.Size
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Cat streams the file at path (root-relative, no leading slash) at rev into w
// while computing its size and SHA-256. It errors if path is a directory or is
// absent — the worker relies on that to enforce "existing file, read-only".
func (r SVNReader) Cat(ctx context.Context, repoPath, path string, rev int64, w io.Writer) (int64, string, error) {
	h := sha256.New()
	counter := &countWriter{}
	sink := io.MultiWriter(w, h, counter)
	if err := runStream(ctx, sink, r.svnlook(), "cat", "-r", strconv.FormatInt(rev, 10), repoPath, path); err != nil {
		return 0, "", err
	}
	return counter.n, hex.EncodeToString(h.Sum(nil)), nil
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func runStream(ctx context.Context, stdout io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, stderrTail(errb.Bytes()))
	}
	return nil
}

func output(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	if err := runStream(ctx, &out, name, args...); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func stderrTail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "(no stderr)"
	}
	const max = 512
	if len(s) > max {
		s = "…" + s[len(s)-max:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

func fileURL(absPath string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}
	return u.String()
}
