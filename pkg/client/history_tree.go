package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// HistoryTreeReader exports a whole tree at a revision for Wehikuł czasu
// (concepts/REPOSITORY_HISTORY_CONCEPT.md §4.3, §5): first a plan written to a
// file, then batches of files under local names the daemon chose. Optional like
// HistoryReader; both steps go through the native helper with one RA session
// each, because a file-by-file export costs an SSH handshake per file.
type HistoryTreeReader interface {
	// HistoryListTree writes the plan of dirURL at revision to planFile, which
	// must not exist, and checks the file against the helper's counts.
	HistoryListTree(ctx context.Context, dirURL string, revision int64, planFile string) (HistoryTreeSummary, error)
	// HistoryFetchTree copies files under dest, an existing directory the caller
	// owns. Every pair is accounted for in the receipt, fetched or skipped.
	HistoryFetchTree(ctx context.Context, rootURL string, revision int64, dest string, pairs []HistoryTreePair) (HistoryTreeReceipt, error)
}

type HistoryTreeSummary struct {
	Revision int64
	Dirs     int64
	Files    int64
	Bytes    int64
}

// HistoryTreeNode is one plan line. Path is relative to the listed directory;
// Size is -1 for a directory.
type HistoryTreeNode struct {
	Path string
	Kind string
	Size int64
}

type HistoryTreePair struct {
	RepoPath  string // relative to rootURL, as the repository names it
	LocalPath string // relative to dest, slash-separated, as the daemon names it
}

type HistoryTreeReceipt struct {
	Files   []HistoryTreeFile
	Skipped []HistoryTreeSkip
}

type HistoryTreeFile struct {
	LocalPath string
	Bytes     int64
}

// HistoryTreeSkip is a file the helper did not write; Reason is "special" for
// a symbolic link, which is reported instead of landing on disk as data.
type HistoryTreeSkip struct {
	LocalPath string
	Reason    string
}

const (
	historyTreePairLimit = nativeCommitTargetLimit / 2
	historyTreeLineLimit = 1 << 20
)

// historyRepoPath accepts a repository-relative name as the repository spells
// it: anything but a climb, an empty segment or a control character.
func historyRepoPath(path string) bool {
	if path == "" || !utf8.ValidString(path) || strings.HasPrefix(path, "/") ||
		strings.ContainsFunc(path, func(r rune) bool { return r < 32 || r == 127 }) {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// historyLocalPath mirrors the helper's working-copy target guard, so a bad
// manifest is refused before a process starts.
func historyLocalPath(path string) bool {
	if !historyRepoPath(path) || strings.ContainsAny(path, `\:`) {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		lower := strings.ToLower(segment)
		if lower == ".svn" || lower == ".filees" || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
			return false
		}
	}
	return true
}

func historyNewFile(path, flag string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("history: %s must be absolute", flag)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("history: %s already exists", flag)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *execClient) HistoryListTree(ctx context.Context, dirURL string, revision int64, planFile string) (HistoryTreeSummary, error) {
	if !c.HistoryEnabled() {
		return HistoryTreeSummary{}, errHistoryUnavailable
	}
	if err := historyTarget(dirURL, revision); err != nil {
		return HistoryTreeSummary{}, err
	}
	if err := historyNewFile(planFile, "plan file"); err != nil {
		return HistoryTreeSummary{}, err
	}
	if err := c.nativeRequireFeature(ctx, "history_tree_v1"); err != nil {
		return HistoryTreeSummary{}, err
	}
	raw, err := c.nativeRemote(ctx, "", "list-tree", "--url", dirURL, "--revision", strconv.FormatInt(revision, 10), "--out", planFile)
	if err != nil {
		return HistoryTreeSummary{}, err
	}
	var summary HistoryTreeSummary
	for field, target := range map[string]*int64{"revision": &summary.Revision, "dirs": &summary.Dirs, "files": &summary.Files, "bytes": &summary.Bytes} {
		if *target, err = nativeRevisionValue(raw, field, false); err != nil {
			return HistoryTreeSummary{}, err
		}
	}
	if summary.Revision != revision {
		return HistoryTreeSummary{}, fmt.Errorf("native SVN: list-tree answered revision %d for %d", summary.Revision, revision)
	}
	var counted HistoryTreeSummary
	if err := EachHistoryTreeNode(planFile, func(node HistoryTreeNode) error {
		if node.Kind == "dir" {
			counted.Dirs++
		} else {
			counted.Files++
			counted.Bytes += node.Size
		}
		return nil
	}); err != nil {
		return HistoryTreeSummary{}, err
	}
	if counted.Dirs != summary.Dirs || counted.Files != summary.Files || counted.Bytes != summary.Bytes {
		return HistoryTreeSummary{}, fmt.Errorf("native SVN: plan holds %d dirs, %d files, %d bytes; receipt says %d, %d, %d",
			counted.Dirs, counted.Files, counted.Bytes, summary.Dirs, summary.Files, summary.Bytes)
	}
	return summary, nil
}

// EachHistoryTreeNode streams a plan file, so a large repository is never held
// in memory just to be read. Names are checked; uniqueness is the planner's
// question, because it has to build sibling sets anyway.
func EachHistoryTreeNode(planFile string, visit func(HistoryTreeNode) error) error {
	file, err := os.Open(planFile)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), historyTreeLineLimit)
	for scanner.Scan() {
		var line struct {
			Path *string `json:"path"`
			Kind string  `json:"kind"`
			Size *int64  `json:"size"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return fmt.Errorf("native SVN: invalid plan line: %w", err)
		}
		if line.Path == nil || !historyRepoPath(*line.Path) {
			return fmt.Errorf("native SVN: unsafe plan path %q", scanner.Text())
		}
		node := HistoryTreeNode{Path: *line.Path, Kind: line.Kind, Size: -1}
		switch {
		case line.Kind == "dir" && line.Size == nil:
		case line.Kind == "file" && line.Size != nil && *line.Size >= 0:
			node.Size = *line.Size
		default:
			return fmt.Errorf("native SVN: plan entry %q has kind %q and size %v", *line.Path, line.Kind, line.Size)
		}
		if err := visit(node); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (c *execClient) HistoryFetchTree(ctx context.Context, rootURL string, revision int64, dest string, pairs []HistoryTreePair) (HistoryTreeReceipt, error) {
	if !c.HistoryEnabled() {
		return HistoryTreeReceipt{}, errHistoryUnavailable
	}
	if err := historyTarget(rootURL, revision); err != nil {
		return HistoryTreeReceipt{}, err
	}
	if !filepath.IsAbs(dest) {
		return HistoryTreeReceipt{}, errors.New("history: destination must be absolute")
	}
	if len(pairs) == 0 || len(pairs) > historyTreePairLimit {
		return HistoryTreeReceipt{}, fmt.Errorf("history: fetch-tree takes 1..%d files per call", historyTreePairLimit)
	}
	wanted := make(map[string]bool, len(pairs))
	input := make([]byte, 0, 64*len(pairs))
	for _, pair := range pairs {
		if !historyRepoPath(pair.RepoPath) || !historyLocalPath(pair.LocalPath) {
			return HistoryTreeReceipt{}, fmt.Errorf("history: unsafe manifest pair %q -> %q", pair.RepoPath, pair.LocalPath)
		}
		if wanted[pair.LocalPath] {
			return HistoryTreeReceipt{}, fmt.Errorf("history: local path %q named twice", pair.LocalPath)
		}
		wanted[pair.LocalPath] = true
		input = append(input, pair.RepoPath...)
		input = append(input, 0)
		input = append(input, pair.LocalPath...)
		input = append(input, 0)
	}
	if len(input) > nativeCommitTargetBytes {
		return HistoryTreeReceipt{}, errors.New("history: manifest exceeds 16 MiB; split the batch")
	}
	if err := c.nativeRequireFeature(ctx, "history_tree_v1"); err != nil {
		return HistoryTreeReceipt{}, err
	}
	raw, err := c.nativeRemoteInput(ctx, "", input, "fetch-tree", "--url", rootURL,
		"--revision", strconv.FormatInt(revision, 10), "--dest", dest, "--manifest-stdin")
	if err != nil {
		return HistoryTreeReceipt{}, err
	}
	return checkHistoryTreeReceipt(raw, revision, dest, wanted)
}

// checkHistoryTreeReceipt believes the disk, not the receipt: every fetched
// file must be there with the receipted size, every skipped file must not.
func checkHistoryTreeReceipt(raw map[string]any, revision int64, dest string, wanted map[string]bool) (HistoryTreeReceipt, error) {
	got, err := nativeRevisionValue(raw, "revision", false)
	if err != nil {
		return HistoryTreeReceipt{}, err
	}
	if got != revision {
		return HistoryTreeReceipt{}, fmt.Errorf("native SVN: fetch-tree answered revision %d for %d", got, revision)
	}
	var doc struct {
		Files *[]struct {
			Path  string `json:"path"`
			Bytes *int64 `json:"bytes"`
		} `json:"files"`
		Skipped *[]struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(nativeJSON(raw)), &doc); err != nil || doc.Files == nil || doc.Skipped == nil {
		return HistoryTreeReceipt{}, fmt.Errorf("native SVN: invalid fetch-tree receipt: %v", err)
	}
	seen := make(map[string]bool, len(wanted))
	account := func(path string) error {
		if !wanted[path] || seen[path] {
			return fmt.Errorf("native SVN: fetch-tree receipt names %q unexpectedly", path)
		}
		seen[path] = true
		return nil
	}
	var receipt HistoryTreeReceipt
	for _, file := range *doc.Files {
		if err := account(file.Path); err != nil {
			return HistoryTreeReceipt{}, err
		}
		info, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(file.Path)))
		if err != nil || file.Bytes == nil || !info.Mode().IsRegular() || info.Size() != *file.Bytes {
			return HistoryTreeReceipt{}, fmt.Errorf("native SVN: fetched %q does not match its receipt", file.Path)
		}
		receipt.Files = append(receipt.Files, HistoryTreeFile{LocalPath: file.Path, Bytes: *file.Bytes})
	}
	for _, skip := range *doc.Skipped {
		if err := account(skip.Path); err != nil {
			return HistoryTreeReceipt{}, err
		}
		if skip.Reason != "special" {
			return HistoryTreeReceipt{}, fmt.Errorf("native SVN: %q skipped for unknown reason %q", skip.Path, skip.Reason)
		}
		if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(skip.Path))); !errors.Is(err, os.ErrNotExist) {
			return HistoryTreeReceipt{}, fmt.Errorf("native SVN: skipped %q is present on disk", skip.Path)
		}
		receipt.Skipped = append(receipt.Skipped, HistoryTreeSkip{LocalPath: skip.Path, Reason: skip.Reason})
	}
	if len(seen) != len(wanted) {
		return HistoryTreeReceipt{}, fmt.Errorf("native SVN: fetch-tree accounted for %d of %d files", len(seen), len(wanted))
	}
	return receipt, nil
}
