package whaleworker

import (
	"bytes"
	"context"
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

	"filees/pkg/repoworker"
	whale "filees/pkg/whale/v1"
)

const getCacheSchema = "filees.whale-get-cache/v1"

type GetSource interface {
	Discover(context.Context, string, string, string, int64) (whale.Identity, int64, error)
	Quote(context.Context, string, whale.Identity, int64) error
	Materialize(context.Context, string, whale.Identity, int64, string) error
}

func (s GetService) Discover(ctx context.Context, clientID string, request whale.Request) (whale.Result, error) {
	if request.Operation != whale.OpGetDiscover {
		return whale.Result{}, errors.New("not a Whale GET discovery")
	}
	if err := request.Validate(); err != nil {
		return whale.Result{}, err
	}
	access, err := s.authorize(ctx, clientID, request.Identity.LogicalRepoID)
	if err != nil {
		return whale.Result{}, err
	}
	if err := s.pruneExpired(); err != nil {
		return whale.Result{}, err
	}
	identity, publishedRevision, err := s.Source.Discover(ctx, access.RepositoryPath, request.Identity.LogicalRepoID, request.Identity.LogicalPath, request.Revision)
	if err != nil {
		return whale.Result{}, err
	}
	return whale.Result{GenerationID: identity.GenerationID, State: whale.StateAwaitingConfirmation, Revision: publishedRevision, ExpectedSize: identity.ExpectedSize, SHA256: identity.SHA256, Identity: &identity}, nil
}

type GetService struct {
	Root         string
	Authority    Authority
	Reservations repoworker.ReservationLedger
	Source       GetSource
	Now          func() time.Time
}

type getRecord struct {
	Schema            string         `json:"schema"`
	ClientID          string         `json:"client_id"`
	TransferID        string         `json:"transfer_id"`
	ConfirmationToken string         `json:"confirmation_token"`
	Identity          whale.Identity `json:"identity"`
	Revision          int64          `json:"revision"`
	ExpiresAt         string         `json:"expires_at"`
}

// Quote proves that the requested immutable tuple exists at the named
// revision. It deliberately creates no cache entry and reserves no space.
func (s GetService) Quote(ctx context.Context, clientID string, request whale.Request) (whale.Result, error) {
	if request.Operation != whale.OpGetQuote {
		return whale.Result{}, errors.New("not a Whale GET quote")
	}
	if err := request.Validate(); err != nil {
		return whale.Result{}, err
	}
	access, err := s.authorize(ctx, clientID, request.Identity.LogicalRepoID)
	if err != nil {
		return whale.Result{}, err
	}
	if err := s.pruneExpired(); err != nil {
		return whale.Result{}, err
	}
	if err := s.Source.Quote(ctx, access.RepositoryPath, request.Identity, request.Revision); err != nil {
		return whale.Result{}, err
	}
	return whale.Result{GenerationID: request.Identity.GenerationID, State: whale.StateAwaitingConfirmation, Revision: request.Revision, ExpectedSize: request.Identity.ExpectedSize, SHA256: request.Identity.SHA256}, nil
}

// ServeWindow materializes the selected revision into a seekable server cache
// only after a confirmation token is present. ready writes the response header
// and streams the requested range while the transfer cache remains pinned.
func (s GetService) ServeWindow(ctx context.Context, clientID string, request whale.Request, ready func(whale.Result, io.Reader) error) (whale.Result, error) {
	if request.Operation != whale.OpGetWindow || ready == nil {
		return whale.Result{}, errors.New("not a Whale GET window")
	}
	if err := request.Validate(); err != nil {
		return whale.Result{}, err
	}
	access, err := s.authorize(ctx, clientID, request.Identity.LogicalRepoID)
	if err != nil {
		return whale.Result{}, err
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return whale.Result{}, err
	}
	if err := s.pruneExpired(); err != nil {
		return whale.Result{}, err
	}
	var result whale.Result
	lockPath := filepath.Join(s.Root, request.TransferID+".lock")
	err = repoworker.WithFileLock(lockPath, func() error {
		record, err := s.loadOrCreate(clientID, request)
		if err != nil {
			return err
		}
		if _, _, expires, err := s.Reservations.Reserve(ctx, request.TransferID, request.Identity.ExpectedSize, s.now()); err != nil {
			return err
		} else {
			record.ExpiresAt = expires.Format(time.RFC3339Nano)
		}
		// Bind the transfer ID and confirmation tuple durably before creating
		// reusable cache content. A crash must not leave an unowned payload
		// which a different confirmation could adopt on retry.
		if err := writeGetRecord(s.recordPath(request.TransferID), record); err != nil {
			return err
		}
		payloadPath := s.payloadPath(request.TransferID)
		if _, err := os.Stat(payloadPath); errors.Is(err, os.ErrNotExist) {
			if err := s.Source.Quote(ctx, access.RepositoryPath, request.Identity, request.Revision); err != nil {
				return err
			}
			if err := s.materialize(ctx, access.RepositoryPath, request, payloadPath); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		file, err := os.Open(payloadPath)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != request.Identity.ExpectedSize {
			return errors.New("Whale GET cache is invalid")
		}
		if _, err := file.Seek(request.Offset, io.SeekStart); err != nil {
			return err
		}
		result = whale.Result{GenerationID: request.Identity.GenerationID, TransferID: request.TransferID, Offset: request.Offset, State: whale.StateMaterializing, Revision: request.Revision, ExpectedSize: request.Identity.ExpectedSize, SHA256: request.Identity.SHA256, PayloadSize: request.PayloadSize, ExpiresAt: record.ExpiresAt}
		return ready(result, io.LimitReader(file, request.PayloadSize))
	})
	return result, err
}

func (s GetService) Release(ctx context.Context, clientID string, request whale.Request) (whale.Result, error) {
	if request.Operation != whale.OpGetRelease {
		return whale.Result{}, errors.New("not a Whale GET release")
	}
	if err := request.Validate(); err != nil {
		return whale.Result{}, err
	}
	if _, err := s.authorize(ctx, clientID, request.Identity.LogicalRepoID); err != nil {
		return whale.Result{}, err
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return whale.Result{}, err
	}
	lockPath := filepath.Join(s.Root, request.TransferID+".lock")
	err := repoworker.WithFileLock(lockPath, func() error {
		record, err := s.load(request.TransferID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && !record.matches(clientID, request) {
			return ErrGenerationConflict
		}
		removeErr := os.RemoveAll(s.transferDir(request.TransferID))
		reserveErr := s.Reservations.Release(request.TransferID)
		return errors.Join(removeErr, reserveErr)
	})
	if err == nil {
		_ = os.Remove(lockPath)
	}
	return whale.Result{GenerationID: request.Identity.GenerationID, TransferID: request.TransferID, State: whale.StateLocal, Revision: request.Revision, ExpectedSize: request.Identity.ExpectedSize, SHA256: request.Identity.SHA256}, err
}

func (s GetService) authorize(ctx context.Context, clientID, repoID string) (RepositoryAccess, error) {
	if !filepath.IsAbs(s.Root) || s.Authority == nil || s.Reservations == nil || s.Source == nil {
		return RepositoryAccess{}, errors.New("Whale GET service is incomplete")
	}
	access, err := s.Authority.ResolveWhale(ctx, clientID, repoID)
	if err != nil || !filepath.IsAbs(access.RepositoryPath) || (access.Access != "r" && access.Access != "rw") {
		return RepositoryAccess{}, ErrAccessDenied
	}
	return access, nil
}

func (s GetService) loadOrCreate(clientID string, request whale.Request) (getRecord, error) {
	record, err := s.load(request.TransferID)
	if err == nil {
		if !record.matches(clientID, request) {
			return getRecord{}, ErrGenerationConflict
		}
		return record, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return getRecord{}, err
	}
	record = getRecord{Schema: getCacheSchema, ClientID: clientID, TransferID: request.TransferID, ConfirmationToken: request.ConfirmationToken, Identity: request.Identity, Revision: request.Revision}
	if err := os.MkdirAll(s.transferDir(request.TransferID), 0o700); err != nil {
		return getRecord{}, err
	}
	return record, nil
}

func (r getRecord) matches(clientID string, request whale.Request) bool {
	return r.Schema == getCacheSchema && r.ClientID == clientID && r.TransferID == request.TransferID && r.ConfirmationToken == request.ConfirmationToken && r.Identity == request.Identity && r.Revision == request.Revision
}

func (s GetService) load(transferID string) (getRecord, error) {
	raw, err := os.ReadFile(s.recordPath(transferID))
	if err != nil {
		return getRecord{}, err
	}
	var record getRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || !record.matches(record.ClientID, whale.Request{TransferID: record.TransferID, ConfirmationToken: record.ConfirmationToken, Identity: record.Identity, Revision: record.Revision}) {
		return getRecord{}, errors.New("invalid Whale GET cache record")
	}
	return record, nil
}

func (s GetService) materialize(ctx context.Context, repositoryPath string, request whale.Request, finalPath string) error {
	tmp, err := os.CreateTemp(filepath.Dir(finalPath), ".payload-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		return err
	}
	defer os.Remove(tmpPath)
	if err := s.Source.Materialize(ctx, repositoryPath, request.Identity, request.Revision, tmpPath); err != nil {
		return err
	}
	digest, size, err := digestFile(tmpPath)
	if err != nil {
		return err
	}
	if size != request.Identity.ExpectedSize || digest != request.Identity.SHA256 {
		return ErrDigestMismatch
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return err
	}
	return syncDir(filepath.Dir(finalPath))
}

func (s GetService) transferDir(id string) string { return filepath.Join(s.Root, id) }
func (s GetService) recordPath(id string) string {
	return filepath.Join(s.transferDir(id), "state.json")
}
func (s GetService) payloadPath(id string) string {
	return filepath.Join(s.transferDir(id), "payload.ready")
}
func (s GetService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// pruneExpired is admission-triggered GC: a dead client cannot pin historical
// materializations forever, and the next quote/window reclaims expired cache
// before a new capacity decision. Live windows refresh the same record before
// streaming and hold the transfer lock while it is read.
func (s GetService) pruneExpired() error {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.now()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := s.load(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			lockPath := filepath.Join(s.Root, entry.Name()+".lock")
			lockErr := repoworker.WithFileLock(lockPath, func() error {
				return errors.Join(os.RemoveAll(s.transferDir(entry.Name())), s.Reservations.Release(entry.Name()))
			})
			if lockErr != nil && !strings.Contains(lockErr.Error(), "already active") {
				return lockErr
			}
			_ = os.Remove(lockPath)
			continue
		}
		if err != nil {
			return err
		}
		expires, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
		if err != nil {
			return errors.New("invalid Whale GET cache expiry")
		}
		if expires.After(now) {
			continue
		}
		lockPath := filepath.Join(s.Root, record.TransferID+".lock")
		err = repoworker.WithFileLock(lockPath, func() error {
			current, loadErr := s.load(record.TransferID)
			if loadErr != nil {
				return loadErr
			}
			currentExpiry, parseErr := time.Parse(time.RFC3339Nano, current.ExpiresAt)
			if parseErr != nil || currentExpiry.After(s.now()) {
				return parseErr
			}
			return errors.Join(os.RemoveAll(s.transferDir(record.TransferID)), s.Reservations.Release(record.TransferID))
		})
		if err != nil && !strings.Contains(err.Error(), "already active") {
			return err
		}
		_ = os.Remove(lockPath)
	}
	return nil
}

func writeGetRecord(path string, record getRecord) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, path)
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}

// SVNGetSource verifies immutable revision metadata and materializes one
// historical repository file without loading it into process memory.
type SVNGetSource struct {
	SVNLook string
}

func (s SVNGetSource) Discover(ctx context.Context, repositoryPath, repoID, logicalPath string, snapshotRevision int64) (whale.Identity, int64, error) {
	if !filepath.IsAbs(s.SVNLook) || !filepath.IsAbs(repositoryPath) || snapshotRevision < 1 {
		return whale.Identity{}, 0, errors.New("Whale SVN GET source is incomplete")
	}
	probe := whale.Identity{LogicalRepoID: repoID, LogicalPath: logicalPath, GenerationID: "00000000-0000-0000-0000-000000000000", ExpectedSize: 1, SHA256: strings.Repeat("0", 64)}
	storagePath, err := probe.StoragePath()
	if err != nil {
		return whale.Identity{}, 0, err
	}
	history, err := s.output(ctx, "history", "--limit", "1", "-r", strconv.FormatInt(snapshotRevision, 10), repositoryPath, storagePath)
	if err != nil {
		return whale.Identity{}, 0, err
	}
	var publishedRevision int64
	for _, line := range strings.Split(string(history), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		candidate, parseErr := strconv.ParseInt(fields[0], 10, 64)
		if parseErr == nil && candidate > 0 {
			publishedRevision = candidate
			break
		}
	}
	if publishedRevision == 0 {
		return whale.Identity{}, 0, errors.New("Whale path has no discoverable revision")
	}
	generation, err := s.output(ctx, "propget", "--revprop", "-r", strconv.FormatInt(publishedRevision, 10), repositoryPath, generationRevprop)
	if err != nil {
		return whale.Identity{}, 0, err
	}
	digest, err := s.output(ctx, "propget", "--revprop", "-r", strconv.FormatInt(publishedRevision, 10), repositoryPath, "filees:whale-sha256")
	if err != nil {
		return whale.Identity{}, 0, err
	}
	sizeRaw, err := s.output(ctx, "filesize", "-r", strconv.FormatInt(snapshotRevision, 10), repositoryPath, storagePath)
	if err != nil {
		return whale.Identity{}, 0, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeRaw)), 10, 64)
	if err != nil {
		return whale.Identity{}, 0, errors.New("Whale discovered size is invalid")
	}
	identity := whale.Identity{LogicalRepoID: repoID, LogicalPath: logicalPath, GenerationID: strings.TrimSpace(string(generation)), ExpectedSize: size, SHA256: strings.TrimSpace(string(digest))}
	if err := identity.Validate(); err != nil {
		return whale.Identity{}, 0, err
	}
	return identity, publishedRevision, nil
}

func (s SVNGetSource) Quote(ctx context.Context, repositoryPath string, identity whale.Identity, revision int64) error {
	if !filepath.IsAbs(s.SVNLook) || !filepath.IsAbs(repositoryPath) || revision < 1 {
		return errors.New("Whale SVN GET source is incomplete")
	}
	storagePath, err := identity.StoragePath()
	if err != nil {
		return err
	}
	generation, err := s.output(ctx, "propget", "--revprop", "-r", strconv.FormatInt(revision, 10), repositoryPath, generationRevprop)
	if err != nil || strings.TrimSpace(string(generation)) != identity.GenerationID {
		return errors.New("Whale generation does not match revision")
	}
	digest, err := s.output(ctx, "propget", "--revprop", "-r", strconv.FormatInt(revision, 10), repositoryPath, "filees:whale-sha256")
	if err != nil || strings.TrimSpace(string(digest)) != identity.SHA256 {
		return ErrDigestMismatch
	}
	sizeRaw, err := s.output(ctx, "filesize", "-r", strconv.FormatInt(revision, 10), repositoryPath, storagePath)
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeRaw)), 10, 64)
	if err != nil || size != identity.ExpectedSize {
		return errors.New("Whale size does not match revision")
	}
	return nil
}

func (s SVNGetSource) Materialize(ctx context.Context, repositoryPath string, identity whale.Identity, revision int64, destination string) error {
	if err := s.Quote(ctx, repositoryPath, identity, revision); err != nil {
		return err
	}
	storagePath, _ := identity.StoragePath()
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, s.SVNLook, "cat", "-r", strconv.FormatInt(revision, 10), repositoryPath, storagePath)
	command.Stdout = file
	var stderr bytes.Buffer
	command.Stderr = &stderr
	runErr := command.Run()
	syncErr := file.Sync()
	closeErr := file.Close()
	if runErr != nil {
		return fmt.Errorf("svnlook cat Whale: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	return errors.Join(syncErr, closeErr)
}

func (s SVNGetSource) output(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, s.SVNLook, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("svnlook Whale metadata: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return raw, nil
}
