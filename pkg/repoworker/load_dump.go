package repoworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
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
	// SpoolRoot holds every intermediate stream of one load (extracted
	// carrier, filtered stream, keep_last_revisions scratch) in a directory
	// unique to this attempt. The pipeline streams through files there
	// instead of holding the dump in memory (LOAD_REPOSITORY_DUMP_CONCEPT.md
	// §5.3).
	SpoolRoot string
	// MaxDumpBytes caps the carrier and each filtered/re-dumped stream.
	// Zero disables this cap; it is not an FSFS or aggregate spool quota.
	MaxDumpBytes int64

	// available reports free bytes on the filesystem holding root. Nil
	// means statfs through FilesystemCapacity; tests replace it.
	available func(ctx context.Context, root string) (int64, error)
}

func (s DumpLoadService) validate() error {
	for name, path := range map[string]string{
		"service working copy": s.ServiceWC, "repositories root": s.RepositoriesRoot,
		"archive dir": s.ArchiveDir, "data authz file": s.DataAuthzFile,
		"svnadmin": s.SVNAdmin, "svnlook": s.SVNLook, "spool root": s.SpoolRoot,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("LOAD_REPOSITORY_DUMP: %s must be an absolute path", name)
		}
	}
	if s.MaxDumpBytes < 0 {
		return errors.New("LOAD_REPOSITORY_DUMP: maximum dump size cannot be negative")
	}
	if root := filepath.Clean(s.SpoolRoot); filepath.Dir(root) == root {
		return errors.New("LOAD_REPOSITORY_DUMP: spool root must be a dedicated directory")
	}
	return nil
}

// Load implements DumpLoader. See LOAD_REPOSITORY_DUMP_CONCEPT.md for the
// full pipeline this mirrors section by section.
func (s DumpLoadService) Load(ctx context.Context, realmID, repoID, operationID string, applyIgnorePolicy bool, keepLastRevisions *int) (LoadedDump, error) {
	if err := s.validate(); err != nil {
		return LoadedDump{}, err
	}
	if keepLastRevisions != nil && *keepLastRevisions < 1 {
		return LoadedDump{}, errors.New("LOAD_REPOSITORY_DUMP: keep_last_revisions must be positive")
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

	if _, _, err := svnrotate.Recover(repoPath, s.ArchiveDir, operationID); err != nil {
		return LoadedDump{}, fmt.Errorf("LOAD_REPOSITORY_DUMP recovery: %w", err)
	}
	if loaded, found, err := s.replayLoad(ctx, repoPath, realmID, repoID, operationID, applyIgnorePolicy, keepLastRevisions); found || err != nil {
		return loaded, err
	}

	// §4: precondition. HEAD must be exactly the carrier commit — this is
	// both the only authorization gate for the destructive side effect and
	// the protection against retrofitting onto a repo with real content.
	carrierName, err := s.carrierName(ctx, repoPath)
	if err != nil {
		return LoadedDump{}, err
	}
	carrierBytes, err := s.carrierSize(ctx, repoPath, carrierName)
	if err != nil {
		return LoadedDump{}, err
	}
	if s.MaxDumpBytes > 0 && carrierBytes > s.MaxDumpBytes {
		return LoadedDump{}, fmt.Errorf("LOAD_REPOSITORY_DUMP: carrier is %d bytes, above repositories.max_dump_size (%d bytes)", carrierBytes, s.MaxDumpBytes)
	}
	spool, err := s.operationSpool(operationID)
	if err != nil {
		return LoadedDump{}, err
	}
	defer os.RemoveAll(spool)
	// Preflight estimate, not a reservation: deltas may expand and other
	// processes can consume capacity after this check.
	if err := s.checkCapacity(ctx, carrierBytes, applyIgnorePolicy, keepLastRevisions != nil); err != nil {
		return LoadedDump{}, err
	}

	dumpPath := filepath.Join(spool, "carrier.dump")
	if err := s.extractCarrier(ctx, repoPath, carrierName, dumpPath, carrierBytes); err != nil {
		return LoadedDump{}, err
	}
	info, err := os.Stat(dumpPath)
	if err != nil {
		return LoadedDump{}, err
	}
	if info.Size() != carrierBytes {
		return LoadedDump{}, errors.New("LOAD_REPOSITORY_DUMP: extracted carrier size differs from r1 metadata")
	}
	if err := checkDumpHeader(dumpPath); err != nil {
		return LoadedDump{}, err
	}
	low, high, err := revisionRangeFile(dumpPath)
	if err != nil {
		return LoadedDump{}, fmt.Errorf("LOAD_REPOSITORY_DUMP: %w", err)
	}
	advance := func(next string) error {
		if err := os.Remove(dumpPath); err != nil {
			return err
		}
		dumpPath = next
		return nil
	}

	toolVersions := map[string]string{"svnadmin": toolVersion(ctx, s.SVNAdmin)}

	if applyIgnorePolicy {
		filtered := filepath.Join(spool, "filtered.dump")
		if err := s.filterIgnored(ctx, dumpPath, filtered); err != nil {
			return LoadedDump{}, err
		}
		if err := advance(filtered); err != nil {
			return LoadedDump{}, err
		}
		toolVersions["svndumpfilter"] = toolVersion(ctx, s.SVNDumpFilter)
		low, high, err = revisionRangeFile(dumpPath)
		if err != nil {
			return LoadedDump{}, fmt.Errorf("LOAD_REPOSITORY_DUMP: %w", err)
		}
	}

	if keepLastRevisions != nil {
		bounded, boundedLow, boundedHigh, err := s.boundToLastRevisions(ctx, dumpPath, *keepLastRevisions, spool)
		if err != nil {
			return LoadedDump{}, err
		}
		if err := advance(bounded); err != nil {
			return LoadedDump{}, err
		}
		low, high = boundedLow, boundedHigh
	}

	loaded := LoadedDump{SourceRevisionRange: fmt.Sprintf("r%d:r%d", low, high), ToolVersions: toolVersions}
	cfg := svnrotate.LoadConfig{RepoPath: repoPath, ArchiveDir: s.ArchiveDir, SVNAdmin: s.SVNAdmin}
	cfg.Prepare = func(staging string, meta svnrotate.Meta) error {
		loaded.OldUUID, loaded.NewUUID = meta.OldUUID, meta.NewUUID
		// Configuration must be installed before the generation is visible.
		if err := writeDataAuthzConf(staging, s.DataAuthzFile); err != nil {
			return err
		}
		return atomicJSON(filepath.Join(staging, "conf", "filees-load-receipt.json"), dumpLoadReceipt{
			Schema: "filees.load-receipt.v1", RealmID: realmID, RepoID: repoID, OperationID: operationID,
			ApplyIgnorePolicy: applyIgnorePolicy, KeepLastRevisions: keepLastRevisions, Result: loaded, Meta: meta,
		})
	}
	dump, err := os.Open(dumpPath)
	if err != nil {
		return LoadedDump{}, err
	}
	defer dump.Close()
	if err := ctx.Err(); err != nil {
		return LoadedDump{}, err
	}
	if _, err := svnrotate.LoadGeneration(cfg, dump, operationID, os.Stderr); err != nil {
		return LoadedDump{}, fmt.Errorf("LOAD_REPOSITORY_DUMP: %w", err)
	}
	return loaded, nil
}

// operationSpool creates a private directory per attempt. It never removes
// existing paths: another attempt or crash residue is not ours to delete.
func (s DumpLoadService) operationSpool(operationID string) (string, error) {
	// Reject invalid operation names even though the actual spool is random.
	if operationID == "" || operationID == "." || !filepath.IsLocal(operationID) || strings.ContainsAny(operationID, `/\`) {
		return "", errors.New("LOAD_REPOSITORY_DUMP: operation id is not a valid spool name")
	}
	if err := os.MkdirAll(s.SpoolRoot, 0o700); err != nil {
		return "", fmt.Errorf("LOAD_REPOSITORY_DUMP spool: %w", err)
	}
	return os.MkdirTemp(s.SpoolRoot, "load-*")
}

// checkCapacity sizes both volumes the load writes to from the carrier size.
// Estimate three carrier-sized budgets with keep_last_revisions (stream,
// scratch FSFS, bounded re-dump), two with the filter alone, one otherwise.
// This is not a size bound: expanded deltas and FSFS overhead can exceed it.
// The new generation is built next to the repositories (svnrotate requires
// the same filesystem for the swap) and is counted as one more copy. When
// both roots share a volume the needs add up.
func (s DumpLoadService) checkCapacity(ctx context.Context, carrierBytes int64, filter, bound bool) error {
	if carrierBytes < 0 {
		return errors.New("LOAD_REPOSITORY_DUMP: negative carrier size")
	}
	copies := int64(1)
	switch {
	case bound:
		copies = 3
	case filter:
		copies = 2
	}
	spoolNeed := loadMargin(saturatingProduct(uint64(carrierBytes), uint64(copies)), carrierBytes)
	repoNeed := loadMargin(carrierBytes, carrierBytes)
	same, err := sameVolume(s.SpoolRoot, s.RepositoriesRoot)
	if err != nil {
		return fmt.Errorf("LOAD_REPOSITORY_DUMP capacity: %w", err)
	}
	if same {
		spoolNeed, repoNeed = saturatingAdd(spoolNeed, carrierBytes), 0
	}
	for _, check := range []struct {
		label, root string
		need        int64
	}{
		{"spool (repositories.load_spool_root)", s.SpoolRoot, spoolNeed},
		{"repositories volume", s.RepositoriesRoot, repoNeed},
	} {
		if check.need == 0 {
			continue
		}
		available, err := s.freeBytes(ctx, check.root)
		if err != nil {
			return fmt.Errorf("LOAD_REPOSITORY_DUMP capacity %s: %w", check.root, err)
		}
		if available < check.need {
			return fmt.Errorf("LOAD_REPOSITORY_DUMP: %s %s has %d bytes free, loading a %d-byte carrier needs about %d", check.label, check.root, available, carrierBytes, check.need)
		}
	}
	return nil
}

func (s DumpLoadService) freeBytes(ctx context.Context, root string) (int64, error) {
	if s.available != nil {
		return s.available(ctx, root)
	}
	available, _, err := FilesystemCapacity{Root: root}.Check(ctx, 0)
	return available, err
}

// loadMargin adds the same headroom capacityDecision keeps for uploads.
func loadMargin(need, carrierBytes int64) int64 {
	margin := int64(64 << 20)
	if carrierBytes/10 > margin {
		margin = carrierBytes / 10
	}
	return saturatingAdd(need, margin)
}

// writeDataAuthzConf restores the canonical svnserve.conf every FileES data
// repository gets, mirroring ServerEffects.CreateFSFS. Prepare installs it
// in staging; svnadmin create's bare defaults point at no authz file.
func writeDataAuthzConf(repoPath, dataAuthzFile string) error {
	if !filepath.IsAbs(dataAuthzFile) {
		return errors.New("data authz path must be absolute")
	}
	conf := []byte("[general]\nanon-access = none\nauth-access = write\nauthz-db = " + dataAuthzFile + "\n")
	return atomicBytes(filepath.Join(repoPath, "conf", "svnserve.conf"), conf)
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

// carrierName enforces the §4 precondition — HEAD must be exactly r1,
// and r1's tree must be exactly one file directly at the repo root — and
// returns that file's name. Both checks run against the repository's own
// current state, never against anything the ticket claims.
func (s DumpLoadService) carrierName(ctx context.Context, repoPath string) (string, error) {
	head, err := s.svnlook(ctx, "youngest", "--", repoPath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(head)) != "1" {
		return "", fmt.Errorf("LOAD_REPOSITORY_DUMP precondition failed: repository HEAD is %q, want exactly r1 (a single carrier commit)", strings.TrimSpace(string(head)))
	}
	tree, err := s.svnlook(ctx, "tree", "--full-paths", "-r", "1", "--", repoPath)
	if err != nil {
		return "", err
	}
	// Normalise line endings before splitting. svnlook emits CRLF on Windows,
	// and splitting on a bare newline leaves a carriage return on every entry -
	// so lines[0] was "/\r", the check failed, and the message blamed the
	// repository tree for what the parser had done to it. The server runs on
	// OpenBSD where this never showed, which is exactly why it is worth removing:
	// the parser should not depend on who spelled the newline.
	text := strings.ReplaceAll(string(tree), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "/" || lines[1] == "" || strings.HasSuffix(lines[1], "/") || strings.Contains(lines[1], "/") {
		return "", fmt.Errorf("LOAD_REPOSITORY_DUMP precondition failed: repository tree at r1 is not exactly one file at the repo root: %q", string(tree))
	}
	return lines[1], nil
}

func (s DumpLoadService) carrierSize(ctx context.Context, repoPath, name string) (int64, error) {
	out, err := s.svnlook(ctx, "filesize", "-r", "1", "--", repoPath, name)
	if err != nil {
		return 0, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("LOAD_REPOSITORY_DUMP: svnlook filesize returned %q", strings.TrimSpace(string(out)))
	}
	return size, nil
}

// extractCarrier streams the carrier file to dst without holding it in memory.
func (s DumpLoadService) extractCarrier(ctx context.Context, repoPath, name, dst string, size int64) error {
	return runToFile(ctx, dst, nil, size, s.SVNLook, "cat", "-r", "1", "--", repoPath, name)
}

func checkDumpHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	const header = "SVN-fs-dump-format-version:"
	head := make([]byte, len(header))
	if _, err := io.ReadFull(f, head); err != nil || string(head) != header {
		return errors.New("LOAD_REPOSITORY_DUMP: carrier does not look like an SVN dump (missing format header)")
	}
	return nil
}

func (s DumpLoadService) svnlook(ctx context.Context, args ...string) ([]byte, error) {
	return dumpSmallOutput(ctx, s.SVNLook, args...)
}

// filterIgnored runs the carrier's dump stream through svndumpfilter,
// translating pkg/filepolicy.BuiltinIgnorePatterns into --pattern arguments
// (LOAD_REPOSITORY_DUMP_CONCEPT.md §6: strip the leading "**/", pass the
// rest — verified live that svndumpfilter's plain "*" already crosses "/").
func (s DumpLoadService) filterIgnored(ctx context.Context, src, dst string) error {
	patterns, err := ignorePatternArgs()
	if err != nil {
		return err
	}
	args := append([]string{"exclude", "--pattern", "--drop-empty-revs"}, patterns...)
	return s.writeDumpStream(ctx, dst, &src, s.SVNDumpFilter, args...)
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

// boundToLastRevisions materializes the dump at src into a scratch FSFS
// (needed only here: a dump stream's own deltas cannot be safely truncated
// as text) and re-dumps its last n revisions, non-incrementally, so the
// result is independently loadable. r0 is never counted; the lower bound is
// always max(1, head-n+1) (LOAD_REPOSITORY_DUMP_CONCEPT.md §5.4 — including
// the verified fact that this does NOT renumber the range to start at r1).
// The scratch repository lives in workDir and is removed once the bounded
// stream exists, so it never coexists with the generation being built.
func (s DumpLoadService) boundToLastRevisions(ctx context.Context, src string, n int, workDir string) (bounded string, low, high int, err error) {
	scratch := filepath.Join(workDir, "scratch.svn")
	defer os.RemoveAll(scratch)
	if err := runDumpTool(ctx, nil, io.Discard, s.SVNAdmin, "create", "--", scratch); err != nil {
		return "", 0, 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", 0, 0, err
	}
	defer in.Close()
	if err := runDumpTool(ctx, in, io.Discard, s.SVNAdmin, "load", "--quiet", "--", scratch); err != nil {
		return "", 0, 0, err
	}
	headOut, err := s.svnlook(ctx, "youngest", "--", scratch)
	if err != nil {
		return "", 0, 0, err
	}
	head, err := strconv.Atoi(strings.TrimSpace(string(headOut)))
	if err != nil || head < 1 || n < 1 {
		return "", 0, 0, fmt.Errorf("scratch youngest or requested range is invalid: %q, n=%d", headOut, n)
	}
	low = head - n + 1
	if low < 1 {
		low = 1
	}
	bounded = filepath.Join(workDir, "bounded.dump")
	if err := s.writeDumpStream(ctx, bounded, nil, s.SVNAdmin, "dump", "--quiet", "-r", fmt.Sprintf("%d:%d", low, head), "--", scratch); err != nil {
		return "", 0, 0, fmt.Errorf("svnadmin dump scratch range: %w", err)
	}
	if err := os.RemoveAll(scratch); err != nil {
		return "", 0, 0, fmt.Errorf("remove scratch before generation load: %w", err)
	}
	return bounded, low, head, nil
}

// revisionRange reads a dump stream record by record: header block, then
// exactly Content-length bytes of body skipped unread. Only a record header
// can report a revision, so file content that happens to contain a line
// "Revision-number: 99" cannot move the recorded range, and the stream is
// never held in memory (LOAD_REPOSITORY_DUMP_CONCEPT.md §5.6: the result
// records what actually happened, not what was asked).
func revisionRange(r io.Reader) (low, high int, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	low, high = -1, -1
	for {
		headers, err := readDumpHeaders(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, err
		}
		if value, ok := headers["Revision-number"]; ok {
			if _, node := headers["Node-path"]; node {
				return 0, 0, errors.New("dump stream mixes revision and node headers")
			}
			n, convErr := strconv.Atoi(value)
			if convErr != nil || n < 0 {
				return 0, 0, fmt.Errorf("dump stream has an invalid Revision-number %q", truncateForError(value))
			}
			if n != 0 { // r0 is the trivial empty root, never counted
				if low == -1 || n < low {
					low = n
				}
				if n > high {
					high = n
				}
			}
		}
		length, err := dumpBodyLength(headers)
		if err != nil {
			return 0, 0, err
		}
		if skipped, err := io.CopyN(io.Discard, br, length); err != nil {
			return 0, 0, fmt.Errorf("dump stream ends inside a record body (%d of %d bytes)", skipped, length)
		}
	}
	if low == -1 {
		return 0, 0, errors.New("dump stream carries no revisions beyond r0")
	}
	return low, high, nil
}

func revisionRangeFile(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	return revisionRange(f)
}

// maxDumpHeaderLine bounds one header line; Node-path is the only long one.
const maxDumpHeaderLine = 1 << 20

// readDumpHeaders skips blank separator lines and returns the next header
// block, or io.EOF when the stream ends cleanly between records.
func readDumpHeaders(br *bufio.Reader) (map[string]string, error) {
	headers := map[string]string{}
	bytesRead := 0
	for {
		line, err := readDumpLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) && len(headers) == 0 && line == "" {
				return nil, io.EOF
			}
			if errors.Is(err, io.EOF) {
				return nil, errors.New("dump stream ends inside a record header")
			}
			return nil, err
		}
		if line == "" {
			if len(headers) == 0 {
				continue
			}
			return headers, nil
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || key == "" {
			return nil, fmt.Errorf("dump stream has a malformed header line %q", truncateForError(line))
		}
		bytesRead += len(line)
		if bytesRead > 2*maxDumpHeaderLine || len(headers) >= 64 {
			return nil, errors.New("dump stream has an oversized header block")
		}
		if _, exists := headers[key]; exists {
			return nil, errors.New("dump stream has a duplicate header")
		}
		headers[key] = strings.TrimPrefix(value, " ")
	}
}

func readDumpLine(br *bufio.Reader) (string, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxDumpHeaderLine {
			return "", errors.New("dump stream has an oversized header line")
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(line) > 0 && errors.Is(err, io.EOF) {
				return "", errors.New("dump stream ends inside a record header")
			}
			return "", err
		}
		return strings.TrimSuffix(string(line[:len(line)-1]), "\r"), nil
	}
}

// dumpBodyLength is Content-length when present; streams that omit it carry
// the property and text lengths separately.
func dumpBodyLength(headers map[string]string) (int64, error) {
	var total int64
	parts := false
	for _, key := range []string{"Prop-content-length", "Text-content-length"} {
		value, ok := headers[key]
		if !ok {
			continue
		}
		parts = true
		n, err := parseDumpLength(key, value)
		if err != nil {
			return 0, err
		}
		if n > math.MaxInt64-total {
			return 0, errors.New("dump body length overflows int64")
		}
		total += n
	}
	if value, ok := headers["Content-length"]; ok {
		n, err := parseDumpLength("Content-length", value)
		if err != nil {
			return 0, err
		}
		if parts && total != n {
			return 0, errors.New("dump body lengths disagree")
		}
		return n, nil
	}
	return total, nil
}

func parseDumpLength(key, value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("dump stream has an invalid %s %q", key, truncateForError(value))
	}
	return n, nil
}

func truncateForError(line string) string {
	if len(line) > 80 {
		return line[:80] + "..."
	}
	return line
}

func toolVersion(ctx context.Context, bin string) string {
	out, err := dumpSmallOutput(ctx, bin, "--version")
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

// The receipt is server-owned configuration, never imported SVN content. It
// travels with the staged generation in the swap, so a new worker can prove
// which operation installed that generation even without a result-store entry.
type dumpLoadReceipt struct {
	Schema            string         `json:"schema"`
	RealmID           string         `json:"realm_id"`
	RepoID            string         `json:"repo_id"`
	OperationID       string         `json:"operation_id"`
	ApplyIgnorePolicy bool           `json:"apply_ignore_policy"`
	KeepLastRevisions *int           `json:"keep_last_revisions"`
	Result            LoadedDump     `json:"result"`
	Meta              svnrotate.Meta `json:"meta"`
}

func (s DumpLoadService) replayLoad(ctx context.Context, repoPath, realm, repo, operation string, ignore bool, keep *int) (LoadedDump, bool, error) {
	raw, err := os.ReadFile(filepath.Join(repoPath, "conf", "filees-load-receipt.json"))
	if errors.Is(err, os.ErrNotExist) {
		return LoadedDump{}, false, nil
	}
	if err != nil {
		return LoadedDump{}, true, err
	}
	var receipt dumpLoadReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return LoadedDump{}, true, err
	}
	sameKeep := receipt.KeepLastRevisions == nil && keep == nil || receipt.KeepLastRevisions != nil && keep != nil && *receipt.KeepLastRevisions == *keep
	if receipt.Schema != "filees.load-receipt.v1" || receipt.RealmID != realm || receipt.RepoID != repo || receipt.OperationID != operation || receipt.ApplyIgnorePolicy != ignore || !sameKeep {
		return LoadedDump{}, true, errors.New("LOAD_REPOSITORY_DUMP: installed generation belongs to a different operation or payload")
	}
	current, err := s.svnlook(ctx, "uuid", repoPath)
	if err != nil {
		return LoadedDump{}, true, err
	}
	meta := receipt.Meta
	if strings.TrimSpace(string(current)) != receipt.Result.NewUUID || meta.NewUUID != receipt.Result.NewUUID || meta.OldUUID != receipt.Result.OldUUID || meta.Reason != operation || meta.Tag == "" || filepath.Base(meta.Tag) != meta.Tag || meta.ArchiveDir != filepath.Join(s.ArchiveDir, meta.Tag+".svn") {
		return LoadedDump{}, true, errors.New("LOAD_REPOSITORY_DUMP: receipt does not match installed generation")
	}
	// Only a finished generation change is a success. A receipt from staging
	// alone must not turn an incomplete archive into a completed operation.
	archiveRaw, err := os.ReadFile(filepath.Join(s.ArchiveDir, meta.Tag+".meta.json"))
	if err != nil {
		return LoadedDump{}, true, err
	}
	var archived svnrotate.Meta
	if err := json.Unmarshal(archiveRaw, &archived); err != nil {
		return LoadedDump{}, true, err
	}
	if archived != meta {
		return LoadedDump{}, true, errors.New("LOAD_REPOSITORY_DUMP: archive metadata differs from receipt")
	}
	if _, err := os.Stat(filepath.Join(meta.ArchiveDir, "FROZEN")); err != nil {
		return LoadedDump{}, true, err
	}
	return receipt.Result, true, nil
}
