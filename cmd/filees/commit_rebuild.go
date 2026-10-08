package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/internal/durable"
	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

const canonicalRecoverySchema = "filees.canonical-wc-recovery/v1"

// The IPC request never stops its own service while holding wcOpMu. Dispatch
// to the supervisor, wait for complete quiescence, then resume through its
// authoritative projection even if the caller cancels after dispatch.
func canonicalRecoveryLifecycle(daemonCtx context.Context, events chan<- provisionedAttachment, recoverWC func(context.Context, config.Repo, client.Client) error) func(context.Context, config.Repo, client.Client) error {
	return func(ctx context.Context, repo config.Repo, svn client.Client) error {
		if events == nil {
			return errors.New("repository attachment lifecycle is unavailable")
		}
		reply := make(chan error, 1)
		select {
		case events <- provisionedAttachment{Repo: repo, Quiesce: true, Result: reply}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case err := <-reply:
			if err != nil {
				return err
			}
		case <-daemonCtx.Done():
			return daemonCtx.Err()
		}
		err := recoverWC(ctx, repo, svn)
		// Even a pre-swap failure restores the old HOLD. No speculative
		// access is granted, and caller cancellation cannot strand the repo.
		select {
		case events <- provisionedAttachment{Repo: repo, Result: reply}:
		case <-daemonCtx.Done():
			return errors.Join(err, daemonCtx.Err())
		}
		select {
		case startErr := <-reply:
			return errors.Join(err, startErr)
		case <-daemonCtx.Done():
			return errors.Join(err, daemonCtx.Err())
		}
	}
}

type canonicalRecoveryJob struct {
	Schema, ID, ServerID, RepoID, RepoURL, Root                               string
	TransactionID, RepositoryUUID, Snapshot, UserSnapshot, Writer, WriterHash string
	Head                                                                      int64
	Expires                                                                   time.Time
	Stage                                                                     string // accepted, prepared, disabled, archived, installed, done, failed
}

type canonicalRecoveryFile struct {
	Path, Hash string
	Size       int64
	Directory  bool
}

// The receipt is outside the replaceable WC. No crash between either rename
// may hide the decision or turn the archived intent into a retryable queue.
func canonicalJobPath(root string) string { return root + ".filees-server-recovery.json" }
func canonicalExists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}
func (j canonicalRecoveryJob) fresh() string {
	return j.Root + ".filees-server-recovery-" + j.ID + "-new"
}
func (j canonicalRecoveryJob) archive() string {
	return j.Root + ".filees-server-recovery-" + j.ID + "-original"
}
func (j canonicalRecoveryJob) copies() string           { return "!kolizje/conflicted-copy-" + j.ID }
func (j canonicalRecoveryJob) disabledMetadata() string { return ".filees-recovery-disabled-" + j.ID }

func validateCanonicalRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return errors.New("canonical recovery requires a specific absolute working-copy directory")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.EqualFold(root, filepath.Clean(home)) {
		return errors.New("canonical recovery refuses the home directory")
	}
	// Also validates parents when root is absent in the rename window.
	return validateIdentityPath(root)
}

func validateCanonicalJob(j canonicalRecoveryJob, repo config.Repo) error {
	_, err := uuid.Parse(j.ID)
	if err != nil || j.Schema != canonicalRecoverySchema || j.Root != filepath.Clean(repo.LocalPath) || j.ServerID != repo.ServerID || j.RepoID != repo.ID || j.RepoURL != repo.RepoURL || j.TransactionID == "" || j.RepositoryUUID == "" || j.Head < 0 || len(j.Snapshot) != 64 || len(j.UserSnapshot) != 64 || len(j.WriterHash) != 64 {
		return errors.New("canonical recovery receipt belongs to another working copy or is invalid")
	}
	switch j.Stage {
	case "accepted", "prepared", "disabled", "archived", "installed", "done", "failed":
	default:
		return errors.New("invalid canonical recovery stage")
	}
	return validateCanonicalRoot(j.Root)
}

func readCanonicalJob(repo config.Repo) (*canonicalRecoveryJob, error) {
	if err := validateCanonicalRoot(filepath.Clean(repo.LocalPath)); err != nil {
		return nil, err
	}
	p := canonicalJobPath(filepath.Clean(repo.LocalPath))
	st, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 1024*1024 {
		return nil, errors.New("invalid canonical recovery receipt file")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var j canonicalRecoveryJob
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, err
	}
	if err := validateCanonicalJob(j, repo); err != nil {
		return nil, err
	}
	return &j, nil
}

// Flush contents before the atomic rename; refuse aliases instead of following
// them. Only our exact receipt is replaced, never an existing staging/archive.
func writeCanonicalJSON(path string, value any) error {
	if err := validateIdentityPath(filepath.Dir(path)); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return errors.New("canonical receipt is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".canonical-receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(path))
}

type canonicalContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r canonicalContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func canonicalHash(ctx context.Context, path string) (string, int64, error) {
	return canonicalProofHash(ctx, path, false)
}

func canonicalProofHash(ctx context.Context, path string, queue bool) (string, int64, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, fmt.Errorf("not a plain file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	if queue {
		raw, err := io.ReadAll(canonicalContextReader{ctx, f})
		if err != nil {
			return "", 0, err
		}
		if proof, err := commit.RecoveryCacheProof(raw); err == nil {
			raw = proof
		}
		// Malformed/unsupported caches remain bound byte-for-byte. Recovery
		// can still preserve them, but cannot silently discard any content.
		h := sha256.Sum256(raw)
		return hex.EncodeToString(h[:]), int64(len(raw)), nil
	}
	h := sha256.New()
	n, err := io.Copy(h, canonicalContextReader{ctx, f})
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Hash every user file, including unversioned work, directories and conflict
// artifacts. Mutable runtime logs/leases are archived intact, not used as a
// content proof. Commit queue/provenance and native owner ARE bound to the plan.
func canonicalSnapshot(ctx context.Context, root string) (string, []canonicalRecoveryFile, error) {
	return canonicalSnapshotMetadata(ctx, root, ".filees")
}

func canonicalSnapshotMetadata(ctx context.Context, root, metadata string) (string, []canonicalRecoveryFile, error) {
	if err := validateCanonicalRoot(root); err != nil {
		return "", nil, err
	}
	var files []canonicalRecoveryFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".svn" || rel == metadata {
			if !d.IsDir() || d.Type()&os.ModeSymlink != 0 {
				return errors.New("recovery refuses aliased metadata")
			}
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 || d.Name() == ".svn" || d.Name() == ".filees" {
			return fmt.Errorf("recovery refuses symlink/nested working copy: %s", rel)
		}
		file := canonicalRecoveryFile{Path: rel, Directory: d.IsDir()}
		if !file.Directory {
			file.Hash, file.Size, err = canonicalHash(ctx, path)
			if err != nil {
				return err
			}
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	proof := append([]canonicalRecoveryFile(nil), files...)
	for _, rel := range []string{".filees/commit_cache/transaction.json", ".filees/commit_cache/previous-transaction.json", ".filees/commit_cache/cache.json", ".filees/state/working-copy.json", ".svn/filees-native-writer-v1"} {
		actual := rel
		if strings.HasPrefix(rel, ".filees/") {
			actual = metadata + strings.TrimPrefix(rel, ".filees")
		}
		path := filepath.Join(root, filepath.FromSlash(actual))
		if err := validateIdentityPath(filepath.Dir(path)); err != nil {
			return "", nil, err
		}
		hash, size, err := canonicalProofHash(ctx, path, rel == ".filees/commit_cache/cache.json")
		if errors.Is(err, os.ErrNotExist) {
			hash, size, err = "absent", 0, nil
		}
		if err != nil {
			return "", nil, err
		}
		proof = append(proof, canonicalRecoveryFile{Path: rel, Hash: hash, Size: size})
	}
	raw, _ := json.Marshal(proof)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), files, nil
}

func canonicalFilesDigest(files []canonicalRecoveryFile) string {
	raw, _ := json.Marshal(files)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func canonicalNativeOwner(ctx context.Context, svn client.Client, root string) (string, error) {
	if inspector, ok := svn.(interface {
		InspectCommitWriter(context.Context, string) (string, error)
	}); ok {
		return inspector.InspectCommitWriter(ctx, root) // refuses an active OS writer guard
	}
	return "", errors.New("canonical recovery requires native writer inspection")
}

func canonicalInfoField(info, key string) string {
	for _, line := range strings.Split(strings.ReplaceAll(info, "\r\n", "\n"), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func planCanonicalWorkingCopy(ctx context.Context, repo config.Repo, svn client.Client, transaction string, head int64) (*canonicalRecoveryJob, *contract.CommitRecoveryPlan, error) {
	if repo.Sparse {
		return nil, nil, errors.New("canonical reconstruction of a sparse attachment is not supported")
	}
	if err := validateWorkingCopyIdentity(repo.LocalPath, expectedWorkingCopyIdentity(repo.ServerID, repo.ID, repo.RepoURL)); err != nil {
		return nil, nil, err
	}
	if prior, err := readCanonicalJob(repo); err != nil {
		return nil, nil, err
	} else if prior != nil && prior.Stage != "done" && prior.Stage != "failed" {
		return nil, nil, errors.New("canonical recovery already requires completion; restart FileES")
	}
	owner, err := canonicalNativeOwner(ctx, svn, repo.LocalPath)
	if err != nil {
		return nil, nil, err
	}
	info, err := svn.GetInfo(ctx, repo.LocalPath)
	if err != nil {
		return nil, nil, err
	}
	repositoryUUID := canonicalInfoField(info, "Repository UUID")
	if !infoHasURL(info, repo.RepoURL) || repositoryUUID == "" {
		return nil, nil, errors.New("canonical recovery repository identity is unavailable")
	}
	snapshot, files, err := canonicalSnapshot(ctx, repo.LocalPath)
	if err != nil {
		return nil, nil, err
	}
	writerHash, _, err := canonicalHash(ctx, filepath.Join(repo.LocalPath, ".svn/filees-native-writer-v1"))
	if err != nil {
		return nil, nil, err
	}
	entries, err := svn.Status(ctx, repo.LocalPath, nil)
	if err != nil {
		return nil, nil, err
	}
	j := &canonicalRecoveryJob{Schema: canonicalRecoverySchema, ID: uuid.NewString(), Root: filepath.Clean(repo.LocalPath), ServerID: repo.ServerID, RepoID: repo.ID, RepoURL: repo.RepoURL, TransactionID: transaction, RepositoryUUID: repositoryUUID, Head: head, Writer: owner, WriterHash: writerHash, Snapshot: snapshot, UserSnapshot: canonicalFilesDigest(files), Expires: time.Now().Add(2 * time.Minute), Stage: "accepted"}
	plan := &contract.CommitRecoveryPlan{PlanID: j.ID, RepoID: repo.ID, TransactionID: transaction, HeadRevision: head, Choice: contract.CommitRecoveryRebuild, ConflictCopy: j.copies(), Archive: j.archive(), ExpiresAt: j.Expires.UTC().Format(time.RFC3339Nano)}
	for _, entry := range entries {
		path := filepath.ToSlash(entry.Path)
		if isFileESControlPath(repo.LocalPath, path) {
			continue
		}
		if entry.Item != "normal" && entry.Item != "none" || entry.Props != "normal" && entry.Props != "none" && entry.Props != "" {
			plan.Paths = append(plan.Paths, path)
		}
		if entry.Conflicted || entry.Item == "conflicted" || entry.Props == "conflicted" {
			plan.Conflicts = append(plan.Conflicts, path)
		}
	}
	return j, plan, nil
}

func acceptCanonicalWorkingCopy(ctx context.Context, repo config.Repo, svn client.Client, j *canonicalRecoveryJob, id string) error {
	if j == nil || j.ID != id || time.Now().After(j.Expires) {
		return errors.New("canonical recovery plan expired or changed")
	}
	if err := validateCanonicalJob(*j, repo); err != nil {
		return err
	}
	owner, err := canonicalNativeOwner(ctx, svn, j.Root)
	if err != nil || owner != j.Writer {
		return errors.New("native writer changed or is active; canonical recovery refused")
	}
	snapshot, _, err := canonicalSnapshot(ctx, j.Root)
	if err != nil {
		return err
	}
	if snapshot != j.Snapshot {
		return errors.New("local files or queue changed; request a new recovery plan")
	}
	return withCanonicalRecoveryLock(canonicalJobPath(j.Root)+".lock", func() error {
		prior, err := readCanonicalJob(repo)
		if err != nil {
			return err
		}
		if prior != nil && prior.Stage != "done" && prior.Stage != "failed" {
			return errors.New("canonical recovery already accepted")
		}
		return writeCanonicalJSON(canonicalJobPath(j.Root), j)
	})
}

func copyCanonicalFile(ctx context.Context, src, dst string, expected canonicalRecoveryFile) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, canonicalContextReader{ctx, input})
	if err == nil {
		err = output.Sync()
	}
	err = errors.Join(err, output.Close())
	if err != nil {
		return err
	}
	hash, size, err := canonicalHash(ctx, dst)
	if err != nil {
		return err
	}
	if hash != expected.Hash || size != expected.Size {
		return errors.New("canonical backup verification failed")
	}
	return durable.SyncDirectory(filepath.Dir(dst))
}

func copyCanonicalLogs(ctx context.Context, original, fresh string) error {
	source := filepath.Join(original, ".filees/logs")
	if !canonicalExists(source) {
		return nil
	}
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("recovery refuses aliased diagnostic logs")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(fresh, ".filees/logs", rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0700)
		}
		hash, size, err := canonicalHash(ctx, path)
		if err != nil {
			return err
		}
		return copyCanonicalFile(ctx, path, dst, canonicalRecoveryFile{Hash: hash, Size: size})
	})
}

func verifyCanonicalCheckout(ctx context.Context, svn client.Client, j canonicalRecoveryJob, root string) error {
	if err := validateCanonicalRoot(root); err != nil {
		return err
	}
	info, err := svn.GetInfo(ctx, root)
	if err != nil {
		return err
	}
	if !infoHasURL(info, j.RepoURL) || !infoHasUUID(info, j.RepositoryUUID) || canonicalInfoField(info, "Depth") != "" && canonicalInfoField(info, "Depth") != "infinity" {
		return errors.New("canonical checkout URL/UUID/depth mismatch")
	}
	rev, err := svn.Revision(ctx, root)
	if err != nil || rev != j.Head {
		return errors.New("canonical checkout revision changed; request a new plan")
	}
	entries, err := svn.Status(ctx, root, nil)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.ToSlash(entry.Path)
		if isFileESControlPath(root, path) {
			continue
		}
		if canonicalInstalled(root, j) && (path == "!kolizje" || strings.HasPrefix(path, "!kolizje/")) && entry.Item == "unversioned" && !entry.Conflicted {
			continue
		}
		if entry.Conflicted || (entry.Item != "normal" && entry.Item != "none") || (entry.Props != "" && entry.Props != "normal" && entry.Props != "none") {
			return fmt.Errorf("canonical checkout is not clean: %s", entry.Path)
		}
	}
	return nil
}

func canonicalMarker(root string) string {
	return filepath.Join(root, ".filees", "state", "canonical-recovery.json")
}
func canonicalInstalled(root string, j canonicalRecoveryJob) bool {
	if err := validateIdentityPath(root); err != nil {
		return false
	}
	st, err := os.Lstat(canonicalMarker(root))
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1024*1024 {
		return false
	}
	raw, err := os.ReadFile(canonicalMarker(root))
	var got canonicalRecoveryJob
	if err != nil || json.Unmarshal(raw, &got) != nil {
		return false
	}
	got.Stage = j.Stage
	if !got.Expires.Equal(j.Expires) {
		return false
	}
	got.Expires = j.Expires // compare wall-clock time, not an in-memory monotonic component
	return got == j
}

func verifyCanonicalCopies(ctx context.Context, root string, j canonicalRecoveryJob) error {
	_, files, err := canonicalSnapshot(ctx, filepath.Join(root, filepath.FromSlash(j.copies())))
	if err != nil {
		return err
	}
	if canonicalFilesDigest(files) != j.UserSnapshot {
		return errors.New("preserved local copies changed or are incomplete")
	}
	return nil
}

// Native live mutations require .filees. Retire ONLY that namespace under the
// writer lease before closing it: Windows cannot rename a WC with an open child
// handle, even SHARE_DELETE. The persisted job owns this reversible transition.
func restoreCanonicalMetadata(root string, j canonicalRecoveryJob) error {
	disabled := filepath.Join(root, j.disabledMetadata())
	if !canonicalExists(disabled) {
		return nil
	}
	st, err := os.Lstat(disabled)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("disabled metadata is not a plain directory")
	}
	if err := validateIdentityPath(disabled); err != nil {
		return err
	}
	if canonicalExists(filepath.Join(root, ".filees")) {
		return errors.New("ambiguous original metadata; recovery refused")
	}
	return withCanonicalWriterLease(filepath.Join(root, ".svn/filees-native-writer-v1"), func(*os.File) error {
		if err := os.Rename(disabled, filepath.Join(root, ".filees")); err != nil {
			return err
		}
		return durable.SyncDirectory(root)
	})
}

// Run only after the entire repository instance (scanner, commit, passport and
// its root guard) stopped. This engine has no Commit/Resolve/WriterRelease call.
// Accepted decisions resume on startup; failed pre-swap work needs a new plan.
func recoverCanonicalWorkingCopy(ctx context.Context, repo config.Repo, svn client.Client) error {
	if err := validateCanonicalRoot(filepath.Clean(repo.LocalPath)); err != nil {
		return err
	}
	return withCanonicalRecoveryLock(canonicalJobPath(filepath.Clean(repo.LocalPath))+".lock", func() error { return runCanonicalWorkingCopyRecovery(ctx, repo, svn) })
}

func runCanonicalWorkingCopyRecovery(ctx context.Context, repo config.Repo, svn client.Client) (resultErr error) {
	j, err := readCanonicalJob(repo)
	if err != nil || j == nil || j.Stage == "done" || j.Stage == "failed" {
		return err
	}
	if repo.Sparse {
		return errors.New("canonical recovery refuses sparse attachments")
	}
	save := func(stage string) error { j.Stage = stage; return writeCanonicalJSON(canonicalJobPath(j.Root), j) }
	// Infer a completed rename from the bound marker, not just path presence.
	if canonicalInstalled(j.Root, *j) && !canonicalExists(j.fresh()) && canonicalExists(j.archive()) {
		if err := restoreCanonicalMetadata(j.archive(), *j); err != nil {
			return err
		}
		if err := verifyCanonicalCheckout(ctx, svn, *j, j.Root); err != nil {
			return err
		}
		if err := verifyCanonicalCopies(ctx, j.Root, *j); err != nil {
			return err
		}
		return save("done")
	}
	defer func() {
		if resultErr != nil && canonicalExists(j.Root) && !canonicalInstalled(j.Root, *j) && !canonicalExists(j.archive()) {
			if err := restoreCanonicalMetadata(j.Root, *j); err != nil {
				resultErr = errors.Join(resultErr, err)
				return
			}
			resultErr = errors.Join(resultErr, save("failed"))
		}
	}()
	if j.Stage == "accepted" {
		if canonicalExists(j.fresh()) || canonicalExists(j.archive()) {
			return errors.New("canonical staging already exists; preserved, request a new plan")
		}
		if _, err := svn.Checkout(ctx, j.RepoURL, j.fresh()); err != nil {
			return fmt.Errorf("checkout canonical server copy: %w", err)
		}
		if err := verifyCanonicalCheckout(ctx, svn, *j, j.fresh()); err != nil {
			return err
		}
		if _, _, err := canonicalSnapshot(ctx, j.fresh()); err != nil {
			return err
		}
		snapshot, files, err := canonicalSnapshot(ctx, j.Root)
		if err != nil {
			return err
		}
		if snapshot != j.Snapshot {
			return errors.New("local data changed during checkout; original WC preserved")
		}
		base := filepath.Join(j.fresh(), filepath.FromSlash(j.copies()))
		if err := os.Mkdir(filepath.Dir(base), 0700); err != nil {
			return err
		}
		if err := os.Mkdir(base, 0700); err != nil {
			return err
		}
		for _, file := range files {
			dst := filepath.Join(base, filepath.FromSlash(file.Path))
			if file.Directory {
				if err := os.MkdirAll(dst, 0700); err != nil {
					return err
				}
			} else if err := copyCanonicalFile(ctx, filepath.Join(j.Root, filepath.FromSlash(file.Path)), dst, file); err != nil {
				return err
			}
		}
		if err := ensureWorkingCopyIdentity(j.fresh(), expectedWorkingCopyIdentity(j.ServerID, j.RepoID, j.RepoURL)); err != nil {
			return err
		}
		for _, rel := range []string{".filees/state/client.uuid", ".filees/state/shout.last_seen"} {
			src := filepath.Join(j.Root, filepath.FromSlash(rel))
			hash, size, err := canonicalHash(ctx, src)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if err := copyCanonicalFile(ctx, src, filepath.Join(j.fresh(), filepath.FromSlash(rel)), canonicalRecoveryFile{Hash: hash, Size: size}); err != nil {
				return err
			}
		}
		if err := copyCanonicalLogs(ctx, j.Root, j.fresh()); err != nil {
			return err
		}
		if err := writeCanonicalJSON(canonicalMarker(j.fresh()), j); err != nil {
			return err
		}
		if err := verifyCanonicalCopies(ctx, j.fresh(), *j); err != nil {
			return err
		}
		if err := save("prepared"); err != nil {
			return err
		}
	}
	if !canonicalInstalled(j.fresh(), *j) {
		return errors.New("canonical staging marker is absent or foreign")
	}
	if err := verifyCanonicalCheckout(ctx, svn, *j, j.fresh()); err != nil {
		return err
	}
	if err := verifyCanonicalCopies(ctx, j.fresh(), *j); err != nil {
		return err
	}
	install := func() error {
		if err := restoreCanonicalMetadata(j.archive(), *j); err != nil {
			return err
		}
		// Once the original moved, finish or roll back even on cancellation.
		if err := os.Rename(j.fresh(), j.Root); err != nil {
			return errors.Join(err, os.Rename(j.archive(), j.Root))
		}
		if err := durable.SyncDirectory(filepath.Dir(j.Root)); err != nil {
			return err
		}
		if err := save("installed"); err != nil {
			return err
		}
		return save("done")
	}
	if canonicalExists(j.Root) {
		if canonicalExists(j.archive()) {
			return errors.New("ambiguous canonical recovery paths; no paths modified")
		}
		metadata := ".filees"
		if canonicalExists(filepath.Join(j.Root, j.disabledMetadata())) {
			if canonicalExists(filepath.Join(j.Root, ".filees")) {
				return errors.New("ambiguous original metadata")
			}
			metadata = j.disabledMetadata()
		}
		snapshot, _, err := canonicalSnapshotMetadata(ctx, j.Root, metadata)
		if err != nil || snapshot != j.Snapshot {
			return errors.New("local data changed; original WC preserved")
		}
		if metadata == ".filees" {
			owner, err := canonicalNativeOwner(ctx, svn, j.Root)
			if err != nil || owner != j.Writer {
				return errors.New("native writer active or changed; original WC preserved")
			}
		}
		remote, ok := svn.(client.TransactionCommitter)
		if !ok {
			return errors.New("canonical recovery requires repository HEAD inspection")
		}
		head, err := remote.CommitHead(ctx, j.RepoURL)
		if err != nil || head != j.Head {
			return errors.New("server changed while preserving local copies; original WC preserved")
		}
		// Disable the native live namespace under the stable inode lease, then
		// close its handle before the root rename. Late helpers cannot enter.
		if err := withCanonicalWriterLease(filepath.Join(j.Root, ".svn/filees-native-writer-v1"), func(f *os.File) error {
			h := sha256.New()
			if _, err := io.Copy(h, f); err != nil {
				return err
			}
			if hex.EncodeToString(h.Sum(nil)) != j.WriterHash {
				return errors.New("native writer record changed before replacement")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if metadata == ".filees" {
				if err := os.Rename(filepath.Join(j.Root, ".filees"), filepath.Join(j.Root, j.disabledMetadata())); err != nil {
					return err
				}
				if err := durable.SyncDirectory(j.Root); err != nil {
					return err
				}
			}
			return save("disabled")
		}); err != nil {
			return err
		}
		if err := moveWorkingCopyWithRetry(ctx, j.Root, j.archive(), os.Rename); err != nil {
			return err
		}
		if err := durable.SyncDirectory(filepath.Dir(j.Root)); err != nil {
			return err
		}
		if err := save("archived"); err != nil {
			return err
		}
	} else if !canonicalExists(j.archive()) {
		return errors.New("original working copy and archive are both absent")
	} else {
		if err := restoreCanonicalMetadata(j.archive(), *j); err != nil {
			return err
		}
		snapshot, _, err := canonicalSnapshot(ctx, j.archive())
		if err != nil || snapshot != j.Snapshot {
			return errors.New("archived original changed; recovery refused")
		}
	}
	return install()
}
