package repoworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"filees/internal/svnrotate"
	"filees/pkg/filepolicy"
)

// LoadedDump is what a successful LOAD_REPOSITORY_DUMP produced, independent
// of the control-plane wire shape (LoadRepositoryDumpResult in
// pkg/control/v1 mirrors this 1:1).
type LoadedDump struct {
	OldUUID, NewUUID    string
	SourceRevisionRange string
	ToolVersions        map[string]string
}

// DumpLoader executes LOAD_REPOSITORY_DUMP end to end: precondition,
// extraction, optional filtering/bounding, and the generation swap via
// internal/svnrotate.LoadGeneration (LOAD_REPOSITORY_DUMP_CONCEPT.md §5).
type DumpLoader interface {
	Load(ctx context.Context, realmID, repoID, operationID string, applyIgnorePolicy bool, keepLastRevisions *int) (LoadedDump, error)
}

// DumpLoadService is the concrete DumpLoader. All paths are absolute,
// following the same discipline as ServerEffects.
type DumpLoadService struct {
	ServiceWC        string
	RepositoriesRoot string
	ArchiveDir       string
	DataAuthzFile    string
	SVNAdmin         string
	SVNLook          string
	SVNDumpFilter    string
}

func (s DumpLoadService) validate() error {
	for name, path := range map[string]string{
		"service working copy": s.ServiceWC, "repositories root": s.RepositoriesRoot,
		"archive dir": s.ArchiveDir, "data authz file": s.DataAuthzFile,
		"svnadmin": s.SVNAdmin, "svnlook": s.SVNLook,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("LOAD_REPOSITORY_DUMP: %s must be an absolute path", name)
		}
	}
	return nil
}

// Load implements DumpLoader. See LOAD_REPOSITORY_DUMP_CONCEPT.md for the
// full pipeline this mirrors section by section.
func (s DumpLoadService) Load(ctx context.Context, realmID, repoID, operationID string, applyIgnorePolicy bool, keepLastRevisions *int) (LoadedDump, error) {
	if err := s.validate(); err != nil {
		return LoadedDump{}, err
	}
	if applyIgnorePolicy && !filepath.IsAbs(s.SVNDumpFilter) {
		return LoadedDump{}, errors.New("LOAD_REPOSITORY_DUMP: apply_current_ignore_policy requires svndumpfilter to be configured")
	}
	if err := s.checkOwnership(repoID, realmID); err != nil {
		return LoadedDump{}, err
	}
	repoPath, err := s.repoPath(repoID)
	if err != nil {
		return LoadedDump{}, err
	}

	// §4: precondition. HEAD must be exactly the carrier commit — this is
	// both the only authorization gate for the destructive side effect and
	// the protection against retrofitting onto a repo with real content.
	carrier, err := s.extractCarrier(ctx, repoPath)
	if err != nil {
		return LoadedDump{}, err
	}
	if !bytes.HasPrefix(carrier, []byte("SVN-fs-dump-format-version:")) {
		return LoadedDump{}, errors.New("LOAD_REPOSITORY_DUMP: carrier does not look like an SVN dump (missing format header)")
	}

	toolVersions := map[string]string{"svnadmin": toolVersion(ctx, s.SVNAdmin)}

	dump := carrier
	if applyIgnorePolicy {
		filtered, err := s.filterIgnored(ctx, dump)
		if err != nil {
			return LoadedDump{}, err
		}
		dump = filtered
		toolVersions["svndumpfilter"] = toolVersion(ctx, s.SVNDumpFilter)
	}

	low, high, err := revisionRange(dump)
	if err != nil {
		return LoadedDump{}, fmt.Errorf("LOAD_REPOSITORY_DUMP: %w", err)
	}

	if keepLastRevisions != nil {
		workDir, err := os.MkdirTemp("", "filees-load-dump-scratch-*")
		if err != nil {
			return LoadedDump{}, err
		}
		defer os.RemoveAll(workDir)
		bounded, boundedLow, boundedHigh, err := s.boundToLastRevisions(ctx, dump, *keepLastRevisions, workDir)
		if err != nil {
			return LoadedDump{}, err
		}
		dump, low, high = bounded, boundedLow, boundedHigh
	}

	cfg := svnrotate.LoadConfig{RepoPath: repoPath, ArchiveDir: s.ArchiveDir}
	meta, err := svnrotate.LoadGeneration(cfg, bytes.NewReader(dump), operationID, os.Stderr)
	if err != nil {
		return LoadedDump{}, fmt.Errorf("LOAD_REPOSITORY_DUMP: %w", err)
	}

	// The new generation's conf/ came from svnadmin create's bare defaults
	// (LoadGeneration does not inherit a carrier's conf/ — it never had any
	// of its own beyond what CreateFSFS wrote). Overwrite it with the same
	// canonical data-authz configuration every repository gets.
	if err := writeDataAuthzConf(repoPath, s.DataAuthzFile); err != nil {
		return LoadedDump{}, fmt.Errorf("LOAD_REPOSITORY_DUMP: %w", err)
	}

	return LoadedDump{
		OldUUID: meta.OldUUID, NewUUID: meta.NewUUID,
		SourceRevisionRange: fmt.Sprintf("r%d:r%d", low, high),
		ToolVersions:        toolVersions,
	}, nil
}

// writeDataAuthzConf restores the canonical svnserve.conf every FileES data
// repository gets, mirroring ServerEffects.CreateFSFS — LoadGeneration built
// the new generation with svnadmin create's bare defaults, which point at
// no authz file at all.
func writeDataAuthzConf(repoPath, dataAuthzFile string) error {
	if !filepath.IsAbs(dataAuthzFile) {
		return errors.New("data authz path must be absolute")
	}
	conf := []byte("[general]\nanon-access = none\nauth-access = write\nauthz-db = " + dataAuthzFile + "\n")
	return os.WriteFile(filepath.Join(repoPath, "conf", "svnserve.conf"), conf, 0o600)
}

func (s DumpLoadService) checkOwnership(repoID, realmID string) error {
	recordPath, err := repositoryRecordPath(s.ServiceWC, repoID)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		return fmt.Errorf("LOAD_REPOSITORY_DUMP: canonical repository record: %w", err)
	}
	var record repositoryRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Schema != RepositorySchema || record.RepoID != repoID {
		return errors.New("LOAD_REPOSITORY_DUMP: canonical repository record is invalid")
	}
	if record.OwnerRealmID != realmID {
		return errors.New("LOAD_REPOSITORY_DUMP: authenticated realm does not own this repository")
	}
	return nil
}

func (s DumpLoadService) repoPath(repoID string) (string, error) {
	path := filepath.Join(s.RepositoriesRoot, repoID)
	if rel, err := filepath.Rel(s.RepositoriesRoot, path); err != nil || rel != repoID {
		return "", errors.New("LOAD_REPOSITORY_DUMP: repository id escapes repositories root")
	}
	return path, nil
}

// extractCarrier enforces the §4 precondition — HEAD must be exactly r1,
// and r1's tree must be exactly one file directly at the repo root — and
// returns that file's bytes. Both checks run against the repository's own
// current state, never against anything the ticket claims.
func (s DumpLoadService) extractCarrier(ctx context.Context, repoPath string) ([]byte, error) {
	head, err := s.svnlook(ctx, "youngest", repoPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(head)) != "1" {
		return nil, fmt.Errorf("LOAD_REPOSITORY_DUMP precondition failed: repository HEAD is %q, want exactly r1 (a single carrier commit)", strings.TrimSpace(string(head)))
	}
	tree, err := s.svnlook(ctx, "tree", "--full-paths", "-r", "1", repoPath)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(tree), "\n"), "\n")
	if len(lines) != 2 || lines[0] != "/" || lines[1] == "" || strings.HasSuffix(lines[1], "/") || strings.Contains(lines[1], "/") {
		return nil, fmt.Errorf("LOAD_REPOSITORY_DUMP precondition failed: repository tree at r1 is not exactly one file at the repo root: %q", string(tree))
	}
	return s.svnlook(ctx, "cat", "-r", "1", repoPath, lines[1])
}

func (s DumpLoadService) svnlook(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.SVNLook, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("svnlook %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// filterIgnored runs the carrier's dump stream through svndumpfilter,
// translating pkg/filepolicy.BuiltinIgnorePatterns into --pattern arguments
// (LOAD_REPOSITORY_DUMP_CONCEPT.md §6: strip the leading "**/", pass the
// rest — verified live that svndumpfilter's plain "*" already crosses "/").
func (s DumpLoadService) filterIgnored(ctx context.Context, dump []byte) ([]byte, error) {
	patterns, err := ignorePatternArgs()
	if err != nil {
		return nil, err
	}
	args := append([]string{"exclude", "--pattern", "--drop-empty-revs"}, patterns...)
	cmd := exec.CommandContext(ctx, s.SVNDumpFilter, args...)
	cmd.Stdin = bytes.NewReader(dump)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("svndumpfilter: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// ignorePatternArgs fails loudly rather than silently under-filtering if a
// future pkg/filepolicy pattern no longer fits the verified "**/<suffix>"
// shape (LOAD_REPOSITORY_DUMP_CONCEPT.md §6).
func ignorePatternArgs() ([]string, error) {
	args := make([]string, 0, len(filepolicy.BuiltinIgnorePatterns))
	for _, p := range filepolicy.BuiltinIgnorePatterns {
		translated, ok := strings.CutPrefix(p, "**/")
		if !ok {
			return nil, fmt.Errorf("ignore pattern %q is outside the verified **/<suffix> shape; svndumpfilter adapter needs re-verification before this pattern can be trusted (LOAD_REPOSITORY_DUMP_CONCEPT.md §6)", p)
		}
		args = append(args, translated)
	}
	return args, nil
}

// boundToLastRevisions materializes dump into a scratch FSFS (needed only
// here: a dump stream's own deltas cannot be safely truncated as text) and
// re-dumps its last n revisions, non-incrementally, so the result is
// independently loadable. r0 is never counted; the lower bound is always
// max(1, head-n+1) (LOAD_REPOSITORY_DUMP_CONCEPT.md §5.4 — including the
// verified fact that this does NOT renumber the range to start at r1).
func (s DumpLoadService) boundToLastRevisions(ctx context.Context, dump []byte, n int, workDir string) (bounded []byte, low, high int, err error) {
	scratch := filepath.Join(workDir, "scratch.svn")
	if out, err := exec.CommandContext(ctx, s.SVNAdmin, "create", scratch).CombinedOutput(); err != nil {
		return nil, 0, 0, fmt.Errorf("svnadmin create scratch: %w: %s", err, out)
	}
	loadCmd := exec.CommandContext(ctx, s.SVNAdmin, "load", "--quiet", scratch)
	loadCmd.Stdin = bytes.NewReader(dump)
	if out, err := loadCmd.CombinedOutput(); err != nil {
		return nil, 0, 0, fmt.Errorf("svnadmin load scratch: %w: %s", err, out)
	}
	headOut, err := s.svnlook(ctx, "youngest", scratch)
	if err != nil {
		return nil, 0, 0, err
	}
	head, err := strconv.Atoi(strings.TrimSpace(string(headOut)))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("scratch youngest: %w", err)
	}
	low = head - n + 1
	if low < 1 {
		low = 1
	}
	var out, errb bytes.Buffer
	dumpCmd := exec.CommandContext(ctx, s.SVNAdmin, "dump", "--quiet", "-r", fmt.Sprintf("%d:%d", low, head), scratch)
	dumpCmd.Stdout = &out
	dumpCmd.Stderr = &errb
	if err := dumpCmd.Run(); err != nil {
		return nil, 0, 0, fmt.Errorf("svnadmin dump scratch range: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), low, head, nil
}

// revisionRange scans a dump stream's own "Revision-number:" headers rather
// than materializing a repository — cheap, and correct regardless of
// whether the numbering is dense (LOAD_REPOSITORY_DUMP_CONCEPT.md §5.6:
// the result records what actually happened, not what was asked).
func revisionRange(dump []byte) (low, high int, err error) {
	const prefix = "Revision-number:"
	low, high = -1, -1
	for _, line := range bytes.Split(dump, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte(prefix)) {
			continue
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(string(line[len(prefix):])))
		if convErr != nil || n == 0 { // r0 is the trivial empty root, never counted
			continue
		}
		if low == -1 || n < low {
			low = n
		}
		if n > high {
			high = n
		}
	}
	if low == -1 {
		return 0, 0, errors.New("dump stream carries no revisions beyond r0")
	}
	return low, high, nil
}

func toolVersion(ctx context.Context, bin string) string {
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}
