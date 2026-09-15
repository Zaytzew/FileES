package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// HistoryReader reads a repository as it was at a revision, for Wehikuł czasu
// (concepts/REPOSITORY_HISTORY_CONCEPT.md). It is optional, like MetadataMover:
// callers type-assert and check HistoryEnabled.
//
// Every read names its revision, and the revision is also the peg, so a URL
// means the object that lived at that name then - including folders a later
// reorganisation removed. There is no HEAD default: a history read that
// quietly answers for "now" answers the wrong question.
//
// History always goes through the native helper, on Linux too. The CLI's
// svn cat and svn export translate line endings for files carrying
// svn:eol-style, so they cannot return the bytes the repository holds.
type HistoryReader interface {
	HistoryEnabled() bool
	HistoryList(ctx context.Context, dirURL string, revision int64) ([]HistoryEntry, error)
	HistoryFetchFile(ctx context.Context, fileURL string, revision int64, out string) (int64, error)
	HistoryRepositoryUUID(ctx context.Context, rootURL string) (string, error)
	// HistoryRevisionAt is the newest revision not later than moment, with its
	// repository date; 0 and "" when nothing was committed that early.
	HistoryRevisionAt(ctx context.Context, rootURL string, moment time.Time) (int64, string, error)
	// HistoryLog returns newest..oldest, newest first, with changed paths.
	HistoryLog(ctx context.Context, rootURL string, newest, oldest int64, limit int) ([]HistoryCommit, error)
}

// HistoryCommit is one revision of a repository log.
type HistoryCommit struct {
	Revision int64
	Date     string
	Author   string
	Message  string
	Changes  []HistoryChange
}

// HistoryChange is one changed path. Path is repository-relative without the
// leading slash ("" is the root); CopyFromRevision is -1 for a plain change.
type HistoryChange struct {
	Path             string
	Action           string
	Kind             string
	CopyFromPath     string
	CopyFromRevision int64
}

// HistoryEntry is one child of a directory at a revision.
type HistoryEntry struct {
	Name string
	// Kind is "file" or "dir".
	Kind string
	// Size is the file size in bytes; -1 for a directory or an unknown size.
	Size                int64
	LastChangedRevision int64
	// LastChangedDate is the repository timestamp as sent by SVN; empty if absent.
	LastChangedDate string
	// LastAuthor is svn:author of LastChangedRevision; empty if absent.
	LastAuthor string
}

var errHistoryUnavailable = errors.New("repository history requires the native SVN helper")

// HistoryPathAbsent reports a history read refused because the path was not
// there, or was not a folder, at the named revision. INCORRECT_PARAMS is
// included because the helper refuses a non-folder listing with it, and
// HistoryList has already refused every malformed argument before the call.
func HistoryPathAbsent(err error) bool {
	var failure *NativeFailure
	if !errors.As(err, &failure) {
		return false
	}
	for _, code := range failure.Codes() {
		switch code {
		case 160013, // SVN_ERR_FS_NOT_FOUND
			160016, // SVN_ERR_FS_NOT_DIRECTORY
			200004, // SVN_ERR_INCORRECT_PARAMS
			200009: // SVN_ERR_ILLEGAL_TARGET
			return true
		}
	}
	return false
}

// HistoryEnabled reports whether a helper is configured. It does not depend on
// nativeWCOps: Linux keeps synchronising through the CLI but reads history
// through the helper.
func (c *execClient) HistoryEnabled() bool { return filepath.IsAbs(c.nativeSVNPath) }

func historyTarget(rawURL string, revision int64) error {
	if revision < 0 {
		return errors.New("history: revision must be explicit and non-negative")
	}
	if !strings.Contains(rawURL, "://") {
		return errors.New("history: target must be a repository URL")
	}
	return nil
}

func (c *execClient) HistoryList(ctx context.Context, dirURL string, revision int64) ([]HistoryEntry, error) {
	if !c.HistoryEnabled() {
		return nil, errHistoryUnavailable
	}
	if err := historyTarget(dirURL, revision); err != nil {
		return nil, err
	}
	if err := c.nativeRequireFeature(ctx, "history_list_v1"); err != nil {
		return nil, err
	}
	raw, err := c.nativeRemote(ctx, "", "list", "--url", dirURL, "--revision", strconv.FormatInt(revision, 10))
	if err != nil {
		return nil, err
	}
	return parseHistoryList(raw, revision)
}

// parseHistoryList trusts nothing about names: a daemon joins them onto a
// destination folder later, so a separator or a dot-segment here would be a
// path escape there.
func parseHistoryList(raw map[string]any, revision int64) ([]HistoryEntry, error) {
	got, err := nativeRevisionValue(raw, "revision", false)
	if err != nil {
		return nil, err
	}
	if got != revision {
		return nil, fmt.Errorf("native SVN: list answered revision %d for %d", got, revision)
	}
	items, ok := raw["entries"].([]any)
	if !ok {
		return nil, errors.New("native SVN: list receipt has no entries")
	}
	out := make([]HistoryEntry, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("native SVN: invalid list entry")
		}
		name, _ := m["name"].(string)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || seen[name] {
			return nil, fmt.Errorf("native SVN: unsafe list entry name %q", name)
		}
		seen[name] = true
		entry := HistoryEntry{Name: name, Size: -1}
		switch m["kind"] {
		case "file":
			entry.Kind = "file"
			if m["size"] != nil {
				if entry.Size, err = nativeRevisionValue(m, "size", false); err != nil {
					return nil, err
				}
			}
		case "dir":
			entry.Kind = "dir"
			if m["size"] != nil {
				return nil, fmt.Errorf("native SVN: directory %q reports a size", name)
			}
		default:
			return nil, fmt.Errorf("native SVN: entry %q has unsupported kind %v", name, m["kind"])
		}
		if entry.LastChangedRevision, err = nativeRevisionValue(m, "last_changed_revision", false); err != nil {
			return nil, err
		}
		if entry.LastChangedRevision > revision {
			return nil, fmt.Errorf("native SVN: entry %q changed in r%d, after the listed r%d", name, entry.LastChangedRevision, revision)
		}
		if date, present := m["last_changed_date"]; present && date != nil {
			if entry.LastChangedDate, ok = date.(string); !ok {
				return nil, fmt.Errorf("native SVN: entry %q has an invalid date", name)
			}
		}
		if author, present := m["last_author"]; present && author != nil {
			if entry.LastAuthor, ok = author.(string); !ok {
				return nil, fmt.Errorf("native SVN: entry %q has an invalid author", name)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// HistoryFetchFile writes one file as the repository stores it. The caller
// owns the unique destination; neither this method nor the helper overwrites.
func (c *execClient) HistoryFetchFile(ctx context.Context, fileURL string, revision int64, out string) (int64, error) {
	if !c.HistoryEnabled() {
		return 0, errHistoryUnavailable
	}
	if err := historyTarget(fileURL, revision); err != nil {
		return 0, err
	}
	if !filepath.IsAbs(out) {
		return 0, errors.New("history: output path must be absolute")
	}
	if _, err := os.Lstat(out); err == nil {
		return 0, errors.New("history: output path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err := c.nativeRequireFeature(ctx, "history_raw_file_v1"); err != nil {
		return 0, err
	}
	raw, err := c.nativeRemote(ctx, "", "fetch-file", "--url", fileURL, "--revision", strconv.FormatInt(revision, 10), "--out", out)
	if err != nil {
		return 0, err
	}
	n, err := nativeRevisionValue(raw, "bytes", false)
	if err != nil {
		return 0, err
	}
	got, err := nativeRevisionValue(raw, "revision", false)
	if err != nil {
		return 0, err
	}
	if got != revision {
		return 0, fmt.Errorf("native SVN: fetch-file answered revision %d for %d", got, revision)
	}
	st, err := os.Lstat(out)
	if err != nil {
		return 0, err
	}
	if !st.Mode().IsRegular() || st.Size() != n {
		return 0, errors.New("native SVN: fetched file does not match its receipt")
	}
	return n, nil
}

// svnTimestamp is the only form the helper accepts inside a {date} revision.
const svnTimestamp = "2006-01-02T15:04:05.000000Z"

func (c *execClient) HistoryRepositoryUUID(ctx context.Context, rootURL string) (string, error) {
	if !c.HistoryEnabled() {
		return "", errHistoryUnavailable
	}
	if err := historyTarget(rootURL, 0); err != nil {
		return "", err
	}
	raw, err := c.nativeRemote(ctx, "", "info", "--url", rootURL)
	if err != nil {
		return "", err
	}
	info, err := parseNativeInfo(raw)
	if err != nil {
		return "", err
	}
	return info.ReposUUID, nil
}

// HistoryRevisionAt lets the repository resolve the date. Paging the log to
// find one number would cost a round trip per page on a long history.
func (c *execClient) HistoryRevisionAt(ctx context.Context, rootURL string, moment time.Time) (int64, string, error) {
	if !c.HistoryEnabled() {
		return 0, "", errHistoryUnavailable
	}
	if err := historyTarget(rootURL, 0); err != nil {
		return 0, "", err
	}
	if moment.IsZero() {
		return 0, "", errors.New("history: moment must be explicit")
	}
	if err := c.nativeRequireFeature(ctx, "history_dated_log_v1"); err != nil {
		return 0, "", err
	}
	stamp := moment.UTC().Format(svnTimestamp)
	raw, err := c.nativeRemote(ctx, "", "log", "--url", rootURL, "--revision", "{"+stamp+"}:0", "--limit", "1")
	if err != nil {
		return 0, "", err
	}
	commits, err := parseHistoryLog(raw, -1, 0)
	if err != nil {
		return 0, "", err
	}
	if len(commits) > 1 {
		return 0, "", errors.New("native SVN: dated log returned more than its limit")
	}
	if len(commits) == 0 || commits[0].Revision == 0 {
		return 0, "", nil
	}
	found := commits[0]
	if date, err := time.Parse(time.RFC3339Nano, found.Date); err != nil || date.After(moment.UTC().Truncate(time.Microsecond)) {
		return 0, "", fmt.Errorf("native SVN: r%d dated %q does not precede %s", found.Revision, found.Date, stamp)
	}
	return found.Revision, found.Date, nil
}

func (c *execClient) HistoryLog(ctx context.Context, rootURL string, newest, oldest int64, limit int) ([]HistoryCommit, error) {
	if !c.HistoryEnabled() {
		return nil, errHistoryUnavailable
	}
	if err := historyTarget(rootURL, oldest); err != nil {
		return nil, err
	}
	if newest < oldest {
		return nil, errors.New("history: log range is reversed")
	}
	if limit < 1 || limit > 100000 {
		return nil, errors.New("history: log limit must be 1..100000")
	}
	raw, err := c.nativeRemote(ctx, "", "log", "--url", rootURL,
		"--revision", fmt.Sprintf("%d:%d", newest, oldest), "--limit", strconv.Itoa(limit), "--changed-paths")
	if err != nil {
		return nil, err
	}
	commits, err := parseHistoryLog(raw, newest, oldest)
	if err != nil {
		return nil, err
	}
	if len(commits) > limit {
		return nil, errors.New("native SVN: log returned more than its limit")
	}
	return commits, nil
}

// parseHistoryLog accepts entries newest first within [oldest, newest]; a
// negative newest leaves the upper bound open. Revision 0 is kept: it is how
// a dated log says the moment precedes every commit.
func parseHistoryLog(raw map[string]any, newest, oldest int64) ([]HistoryCommit, error) {
	if _, ok := raw["entries"].([]any); !ok {
		return nil, errors.New("native SVN: log receipt has no entries")
	}
	var doc struct {
		Entries []struct {
			Revision *int64  `json:"revision"`
			Author   *string `json:"author"`
			Date     *string `json:"date"`
			Message  *string `json:"message"`
			Paths    []struct {
				Path         string  `json:"path"`
				Action       string  `json:"action"`
				Kind         *string `json:"kind"`
				CopyfromPath *string `json:"copyfrom_path"`
				CopyfromRev  *int64  `json:"copyfrom_rev"`
			} `json:"paths"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(nativeJSON(raw)), &doc); err != nil {
		return nil, fmt.Errorf("native SVN: invalid log receipt: %w", err)
	}
	out := make([]HistoryCommit, 0, len(doc.Entries))
	previous := int64(-1)
	for _, entry := range doc.Entries {
		if entry.Revision == nil {
			return nil, errors.New("native SVN: log entry without a revision")
		}
		rev := *entry.Revision
		if rev < oldest || (newest >= 0 && rev > newest) || (previous >= 0 && rev >= previous) {
			return nil, fmt.Errorf("native SVN: log entry r%d outside or out of order", rev)
		}
		previous = rev
		commit := HistoryCommit{Revision: rev}
		if entry.Author != nil {
			commit.Author = *entry.Author
		}
		if entry.Message != nil {
			commit.Message = *entry.Message
		}
		if entry.Date != nil {
			if _, err := time.Parse(time.RFC3339Nano, *entry.Date); err != nil {
				return nil, fmt.Errorf("native SVN: r%d has an invalid date %q", rev, *entry.Date)
			}
			commit.Date = *entry.Date
		}
		for _, p := range entry.Paths {
			path, ok := historyLogPath(p.Path)
			if !ok {
				return nil, fmt.Errorf("native SVN: r%d has an unsafe changed path %q", rev, p.Path)
			}
			change := HistoryChange{Path: path, CopyFromRevision: -1}
			switch p.Action {
			case "A", "D", "M", "R":
				change.Action = p.Action
			default:
				return nil, fmt.Errorf("native SVN: r%d has an unknown action %q", rev, p.Action)
			}
			if p.Kind != nil && (*p.Kind == "file" || *p.Kind == "dir") {
				change.Kind = *p.Kind
			}
			if (p.CopyfromPath == nil) != (p.CopyfromRev == nil) {
				return nil, fmt.Errorf("native SVN: r%d has half a copy source for %q", rev, p.Path)
			}
			if p.CopyfromPath != nil {
				from, ok := historyLogPath(*p.CopyfromPath)
				if !ok || *p.CopyfromRev < 0 || *p.CopyfromRev >= rev {
					return nil, fmt.Errorf("native SVN: r%d has an invalid copy source for %q", rev, p.Path)
				}
				change.CopyFromPath, change.CopyFromRevision = from, *p.CopyfromRev
			}
			commit.Changes = append(commit.Changes, change)
		}
		out = append(out, commit)
	}
	return out, nil
}

// historyLogPath turns a repository path ("/a/b") into a relative one. Names
// are not otherwise judged: the repository may hold names this platform
// cannot store, and history must still show them.
func historyLogPath(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, 0) {
		return "", false
	}
	rel := strings.TrimPrefix(raw, "/")
	if rel == "" {
		return "", true
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
	}
	return rel, true
}
