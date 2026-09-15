package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
