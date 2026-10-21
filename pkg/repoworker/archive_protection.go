package repoworker

// Repository-local enforcement for the archival-policy gate. This is not a
// control endpoint: canonical authority/projection and a persistent admission
// fence must be integrated before exposing freeze to clients. In particular,
// a successful local freeze is NOT an archive-operation acknowledgement.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ArchiveGuardVersion   = "filees.archive-protection/v1"
	archiveGuardDirectory = ".filees-archive-protection"
	archiveGuardRecord    = "policy.json"
)

var (
	ErrArchiveReadOnly = errors.New("archive is read-only; use a controlled server-side append")
	ErrArchiveLocks    = errors.New("archive freeze is waiting for existing locks to be released")
)

// ArchiveProtectionRecord binds the local guard to one logical repository,
// UUID and confirmed HEAD. Its audit chain is retained after administrative
// release. The directory is also a required-state sentinel: losing policy.json
// inside it must never restore write access.
type ArchiveProtectionRecord struct {
	Schema         string    `json:"schema"`
	RepoID         string    `json:"repo_id"`
	RepositoryUUID string    `json:"repository_uuid"`
	OperationID    string    `json:"operation_id"`
	Epoch          uint64    `json:"epoch"`
	State          string    `json:"state"` // freezing, archived, released
	Revision       int64     `json:"revision"`
	Reason         string    `json:"reason,omitempty"`
	PreviousSHA256 string    `json:"previous_sha256,omitempty"`
	At             time.Time `json:"at"`
}

func (r ArchiveProtectionRecord) validate() error {
	if r.Schema != ArchiveGuardVersion || r.Epoch == 0 || r.Revision < 0 || r.At.IsZero() {
		return errors.New("invalid archive protection record")
	}
	for _, id := range []string{r.RepoID, r.RepositoryUUID, r.OperationID} {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return errors.New("archive protection requires canonical UUIDs")
		}
	}
	if r.State != "freezing" && r.State != "archived" && r.State != "released" {
		return errors.New("invalid archive protection state")
	}
	if len(r.Reason) > 1024 || strings.ContainsRune(r.Reason, 0) || (r.State == "released" && (strings.TrimSpace(r.Reason) == "" || r.PreviousSHA256 == "")) {
		return errors.New("administrative archive release requires a bounded reason")
	}
	if r.PreviousSHA256 != "" {
		if value, err := hex.DecodeString(r.PreviousSHA256); err != nil || len(value) != sha256.Size || strings.ToLower(r.PreviousSHA256) != r.PreviousSHA256 {
			return errors.New("invalid preceding archive protection digest")
		}
	}
	return nil
}

// ArchiveProtection is an internal server primitive. The caller resolves the
// path from canonical authority, holds worker/service-WC locks, and supplies
// the existing acknowledged all-session barrier (including foreign realms).
// No caller-supplied command or client bypass token is supported.
type ArchiveProtection struct {
	Repository, Executable, SVNLook, SVNAdmin string
	WithSessionsStopped                       func(context.Context, func() error) error
	Now                                       func() time.Time
}

// A barrier adapter must call its action exactly once and propagate failure.
// Do not accept an adapter that returns success without entering the action,
// or swallows an inspection/write error. Session quiescence itself remains the
// responsibility of the existing activation barrier, not this assertion.
func (p ArchiveProtection) withBarrier(ctx context.Context, action func() error) error {
	calls := 0
	var actionErr error
	err := p.WithSessionsStopped(ctx, func() error {
		calls++
		if calls != 1 {
			return errors.New("archive session barrier entered twice")
		}
		if actionErr = ctx.Err(); actionErr != nil {
			return actionErr
		}
		actionErr = action()
		return actionErr
	})
	if err != nil {
		return err
	}
	if calls != 1 {
		return errors.New("archive session barrier did not enter exactly once")
	}
	if actionErr != nil {
		return actionErr
	}
	return ctx.Err()
}

func (p ArchiveProtection) paths() (string, string, error) {
	hooks, err := lockGuardDirectory(p.Repository)
	if err != nil {
		return "", "", err
	}
	if parsed, err := uuid.Parse(filepath.Base(p.Repository)); err != nil || parsed.String() != filepath.Base(p.Repository) {
		return "", "", errors.New("archive protection repository must have its canonical ID")
	}
	root := filepath.Join(hooks, archiveGuardDirectory)
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("archive protection state must be a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return hooks, root, nil
}

func readArchiveProtection(repository string) (*ArchiveProtectionRecord, error) {
	_, root, err := (ArchiveProtection{Repository: repository}).paths()
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	path := filepath.Join(root, archiveGuardRecord)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return nil, errors.New("archive protection record is not a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 8193))
	decoder.DisallowUnknownFields()
	var record ArchiveProtectionRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing archive protection data")
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	if record.RepoID != filepath.Base(repository) {
		return nil, errors.New("archive protection belongs to another repository")
	}
	auditPath := filepath.Join(root, fmt.Sprintf("%020d-%s.json", record.Epoch, record.State))
	audit, err := readArchiveAudit(auditPath)
	if err != nil || audit != record {
		return nil, errors.New("archive protection audit is missing or inconsistent")
	}
	// FSFS stores the logical UUID on its first line. Do not start a native
	// process in a hook just to inspect identity. A swap with another UUID
	// must fail closed, including when the guard has been administratively released.
	uuidFile, err := os.Open(filepath.Join(repository, "db", "uuid"))
	if err != nil {
		return nil, err
	}
	defer uuidFile.Close()
	raw, err := io.ReadAll(io.LimitReader(uuidFile, 4096))
	if err != nil || strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0]) != record.RepositoryUUID {
		return nil, errors.New("archive protection repository UUID changed")
	}
	return &record, nil
}

// CheckArchiveWrite is shared by final commit and lock admission. Legacy
// repositories without the sentinel are unchanged; a damaged required state
// refuses rather than being interpreted as an ordinary repository.
func CheckArchiveWrite(repository string) error {
	// Ordinary hooks predate UUID-named FileES repositories (e.g. the service
	// repository). They have no sentinel and must keep their existing behavior.
	if !filepath.IsAbs(repository) {
		return errors.New("archive guard requires an absolute repository")
	}
	root := filepath.Join(repository, "hooks", archiveGuardDirectory)
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	r, err := readArchiveProtection(repository)
	if err != nil {
		return fmt.Errorf("cannot establish archive protection: %w", err)
	}
	if r == nil || r.State != "released" {
		return ErrArchiveReadOnly
	}
	return nil
}

// Archive revprops are never mutable through the hook, even after release:
// an ordinary SVN repository denies revprop changes by default too.
func RunArchiveRevpropGuard(args []string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "FileES: revision property changes require a separate administrative procedure.")
	return 1
}

func checkArchiveGuardExecutable(executable string) error {
	if err := CheckLockGuardExecutable(executable); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, executable, "--archive-guard-version").Output()
	if err != nil || string(out) != ArchiveGuardVersion+"\n" {
		return errors.New("worker does not support archive protection")
	}
	return nil
}

func archiveGuardHooks() []string {
	return []string{"pre-commit", "pre-lock", "pre-unlock", "pre-revprop-change"}
}

func inspectArchiveHooks(hooks, executable string, require bool) error {
	for _, name := range archiveGuardHooks() {
		path := filepath.Join(hooks, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && !require {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("preserving existing %s: explicit hook integration required", name)
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if target != executable {
			return fmt.Errorf("preserving existing %s: explicit hook integration required", name)
		}
	}
	return nil
}

func (p ArchiveProtection) snapshot(ctx context.Context) (RepositoryRevision, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, path := range []string{p.SVNLook, p.SVNAdmin} {
		if !filepath.IsAbs(path) {
			return RepositoryRevision{}, false, errors.New("archive tools must be absolute")
		}
	}
	u, err := runLockAuthorityCommand(ctx, p.SVNLook, "uuid", p.Repository)
	if err != nil {
		return RepositoryRevision{}, false, err
	}
	h, err := runLockAuthorityCommand(ctx, p.SVNLook, "youngest", p.Repository)
	if err != nil {
		return RepositoryRevision{}, false, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(h)), 10, 64)
	if err != nil || n < 0 {
		return RepositoryRevision{}, false, errors.New("invalid archive HEAD")
	}
	command := exec.CommandContext(ctx, p.SVNAdmin, "lslocks", p.Repository)
	command.Dir = p.Repository
	command.Env = append(os.Environ(), "LC_ALL=C")
	command.WaitDelay = time.Second
	var locks archiveLockPresence
	command.Stdout = &locks
	err = command.Run()
	if err != nil {
		return RepositoryRevision{}, false, err
	}
	// Empty output is proof of no locks; any output is conservatively busy.
	return RepositoryRevision{UUID: strings.TrimSpace(string(u)), Number: n}, locks.present, nil
}

// Existence, not the entire human-readable lock inventory. Keep memory bounded
// even when an archive candidate has a very large number of active locks.
type archiveLockPresence struct{ present bool }

func (p *archiveLockPresence) Write(raw []byte) (int, error) {
	p.present = p.present || len(bytes.TrimSpace(raw)) != 0
	return len(raw), nil
}

// Freeze installs only recognized hooks. An interrupted installation retains
// freezing and is resumable with the same binding. Existing locks are never
// broken: ordinary unlock stays available while new locks/commits are refused.
// This primitive does not publish canonical authority or acknowledge a client.
func (p ArchiveProtection) Freeze(ctx context.Context, want ArchiveProtectionRecord) error {
	if !FileLocksSupported() {
		return errors.New("archive transitions require supported Unix server file locks")
	}
	want.Schema, want.State, want.Reason = ArchiveGuardVersion, "freezing", ""
	if p.Now != nil {
		want.At = p.Now().UTC()
	} else {
		want.At = time.Now().UTC()
	}
	if err := want.validate(); err != nil {
		return err
	}
	if p.WithSessionsStopped == nil {
		return errors.New("archive freeze requires an acknowledged session barrier")
	}
	if err := checkArchiveGuardExecutable(p.Executable); err != nil {
		return err
	}
	hooks, root, err := p.paths()
	if err != nil {
		return err
	}
	if want.RepoID != filepath.Base(p.Repository) {
		return errors.New("archive plan repository mismatch")
	}
	return WithFileLockContext(ctx, filepath.Join(hooks, ".filees-lock-guards.lock"), func() error {
		// A foreign hook must cause refusal before creating the sentinel.
		if err := inspectArchiveHooks(hooks, p.Executable, false); err != nil {
			return err
		}
		return p.withBarrier(ctx, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			head, _, err := p.snapshot(ctx)
			if err != nil {
				return err
			}
			if head.UUID != want.RepositoryUUID || head.Number != want.Revision {
				return errors.New("archive plan revision or UUID changed")
			}
			existing, err := readArchiveProtection(p.Repository)
			if err != nil {
				return err
			}
			if existing != nil && existing.State == "released" {
				if existing.Epoch == ^uint64(0) || want.Epoch != existing.Epoch+1 || want.OperationID == existing.OperationID {
					return errors.New("archive refreeze requires a new operation and policy epoch")
				}
				raw, err := os.ReadFile(filepath.Join(root, archiveGuardRecord))
				if err != nil {
					return err
				}
				digest := sha256.Sum256(raw)
				want.PreviousSHA256 = hex.EncodeToString(digest[:])
				if err := writeArchiveProtection(root, want); err != nil {
					return err
				}
			} else if existing != nil {
				if existing.OperationID != want.OperationID || existing.Epoch != want.Epoch || existing.RepositoryUUID != want.RepositoryUUID || existing.Revision != want.Revision {
					return errors.New("archive protection belongs to another policy transition")
				}
				want = *existing
			} else {
				if want.Epoch != 1 || want.PreviousSHA256 != "" {
					return errors.New("initial archive protection requires its first policy epoch")
				}
				if err := os.Mkdir(root, 0755); err != nil {
					return err
				}
				if err := syncDirectory(hooks); err != nil {
					return err
				}
				if err := writeArchiveProtection(root, want); err != nil {
					return err
				}
			}
			for _, name := range archiveGuardHooks() {
				path := filepath.Join(hooks, name)
				if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
					if err := os.Symlink(p.Executable, path); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
			}
			if err := syncDirectory(hooks); err != nil {
				return err
			}
			if err := inspectArchiveHooks(hooks, p.Executable, true); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			head, busy, err := p.snapshot(ctx)
			if err != nil {
				return err
			}
			if head.UUID != want.RepositoryUUID || head.Number != want.Revision {
				return errors.New("archive boundary changed during installation")
			}
			if busy {
				return ErrArchiveLocks
			}
			if want.State == "archived" {
				return nil
			}
			want.State = "archived"
			return writeArchiveProtection(root, want)
		})
	})
}

// Release is intentionally not exposed through user control/GUI. A future
// administrative adapter must authorize it separately and publish a new policy
// epoch under the same barrier. Failure before local release remains read-only;
// failure after it is an auditable interrupted administrative transition, not
// an assertion that canonical publication succeeded.
func (p ArchiveProtection) Release(ctx context.Context, expected ArchiveProtectionRecord, operationID, reason string) error {
	if !FileLocksSupported() {
		return errors.New("archive transitions require supported Unix server file locks")
	}
	if p.WithSessionsStopped == nil {
		return errors.New("archive release requires an acknowledged session barrier")
	}
	hooks, root, err := p.paths()
	if err != nil {
		return err
	}
	return WithFileLockContext(ctx, filepath.Join(hooks, ".filees-lock-guards.lock"), func() error {
		return p.withBarrier(ctx, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			current, err := readArchiveProtection(p.Repository)
			if err != nil {
				return err
			}
			if err := expected.validate(); err != nil {
				return err
			}
			if current == nil || expected.State == "released" || expected.Epoch == ^uint64(0) {
				return errors.New("archive release binding changed")
			}
			if err := inspectArchiveHooks(hooks, p.Executable, true); err != nil {
				return err
			}
			raw, err := json.MarshalIndent(expected, "", "  ")
			if err != nil {
				return err
			}
			digest := sha256.Sum256(append(raw, '\n'))
			if current.State == "released" && current.OperationID == operationID && current.Reason == reason && current.Epoch == expected.Epoch+1 && current.PreviousSHA256 == hex.EncodeToString(digest[:]) {
				return nil // lost response: never repeat the administrative effect
			}
			if *current != expected {
				return errors.New("archive release binding changed")
			}
			next := *current
			next.State, next.OperationID, next.Reason = "released", operationID, reason
			next.PreviousSHA256, next.Epoch = hex.EncodeToString(digest[:]), current.Epoch+1
			if p.Now != nil {
				next.At = p.Now().UTC()
			} else {
				next.At = time.Now().UTC()
			}
			if err := next.validate(); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return writeArchiveProtection(root, next)
		})
	})
}

func writeArchiveProtection(root string, record ArchiveProtectionRecord) error {
	if err := record.validate(); err != nil {
		return err
	}
	// atomicBytes defaults to 0600; hooks execute as the data user. The state
	// contains only policy metadata. Set permissions before the atomic rename,
	// not afterwards, so no acknowledged transition has an unreadable window.
	event := filepath.Join(root, fmt.Sprintf("%020d-%s.json", record.Epoch, record.State))
	if prior, err := readArchiveAudit(event); err == nil {
		// The same bound transition can have survived a crash just before the
		// active-pointer rename. Preserve its original timestamp on retry.
		record.At = prior.At
		if prior != record {
			return errors.New("conflicting archive protection audit event")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := archiveProtectionTemp(root, raw)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	// Keep an immutable event before changing the active pointer. A matching
	// file is an idempotent retry; conflicting history is never overwritten.
	if existing, err := os.ReadFile(event); err == nil {
		if !bytes.Equal(existing, raw) {
			return errors.New("conflicting archive protection audit event")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Link(tmp, event); err != nil {
			return err
		}
		if err := syncDirectory(root); err != nil {
			return err
		}
	} else {
		return err
	}
	// Keep the audit and current record on different inodes. A damaged active
	// file must not also change its supposed independent audit through a link.
	pointer, err := archiveProtectionTemp(root, raw)
	if err != nil {
		return err
	}
	defer os.Remove(pointer)
	if err := os.Rename(pointer, filepath.Join(root, archiveGuardRecord)); err != nil {
		return err
	}
	return syncDirectory(root)
}

func archiveProtectionTemp(root string, raw []byte) (string, error) {
	f, err := os.CreateTemp(root, ".policy-*.tmp")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err = f.Chmod(0644); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func readArchiveAudit(path string) (ArchiveProtectionRecord, error) {
	var record ArchiveProtectionRecord
	info, err := os.Lstat(path)
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return record, errors.New("archive audit is not a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return record, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return record, err
	}
	if len(raw) > 8192 {
		return record, errors.New("archive audit exceeded its limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return record, errors.New("trailing archive audit data")
	}
	return record, record.validate()
}
