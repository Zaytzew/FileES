package whaleworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/pkg/repoworker"
	whale "filees/pkg/whale/v1"
)

var (
	ErrAccessDenied   = errors.New("Whale access denied")
	ErrOffsetConflict = errors.New("Whale durable offset conflict")
	ErrDigestMismatch = errors.New("Whale digest mismatch")
)

type RepositoryAccess struct {
	RepositoryPath string
	Access         string
}

type Authority interface {
	ResolveWhale(context.Context, string, string) (RepositoryAccess, error)
}

type Publisher interface {
	PublishWhale(context.Context, *Record, Journal, string, string) (int64, error)
}

type PutService struct {
	Journal      Journal
	Authority    Authority
	Reservations repoworker.ReservationLedger
	Publisher    Publisher
	Queue        PathQueue
	Now          func() time.Time
}

func (s PutService) ReceiveWindow(ctx context.Context, clientID string, request whale.Request, payload io.Reader) (whale.PutResult, error) {
	return s.ServeWindow(ctx, clientID, request, payload, nil)
}

// ServeWindow invokes ready only after authority, FIFO ownership, capacity and
// the exact durable offset are verified. The SSH adapter uses it for the
// server's OFFSET handshake before the client starts sending payload bytes.
func (s PutService) ServeWindow(ctx context.Context, clientID string, request whale.Request, payload io.Reader, ready func(whale.PutResult) error) (whale.PutResult, error) {
	if request.Operation != whale.OpPutWindow {
		return whale.PutResult{}, errors.New("not a Whale PUT window")
	}
	if err := request.Validate(); err != nil {
		return whale.PutResult{}, err
	}
	if _, err := s.authorize(ctx, clientID, request.Identity.LogicalRepoID, true); err != nil {
		return whale.PutResult{}, err
	}
	record, err := s.Journal.Create(request.Identity, whale.DirectionPut)
	if err != nil {
		return whale.PutResult{}, err
	}
	if record.State != whale.StateReceiving {
		return result(record), ErrOffsetConflict
	}
	position, err := s.Queue.Claim(request.Identity)
	if err != nil {
		return whale.PutResult{}, err
	}
	if position != 0 {
		return whale.PutResult{}, BusyError{Position: position}
	}
	lockPath := filepath.Join(s.Journal.generationDir(record.Identity.GenerationID), ".window.lock")
	err = repoworker.WithFileLock(lockPath, func() error {
		current, err := s.requireRecord(request.Identity)
		if err != nil {
			return err
		}
		record = current
		if record.State != whale.StateReceiving || record.BytesHave != request.Offset {
			return ErrOffsetConflict
		}
		if _, _, _, err := s.Reservations.Reserve(ctx, record.Identity.GenerationID, record.Identity.ExpectedSize, s.now()); err != nil {
			return err
		}
		if ready != nil {
			if err := ready(result(record)); err != nil {
				return err
			}
		}
		payloadPath, err := s.Journal.PayloadPath(record.Identity.GenerationID)
		if err != nil {
			return err
		}
		if err := appendWindow(payloadPath, record.BytesHave, request.PayloadSize, payload); err != nil {
			return err
		}
		record.BytesHave += request.PayloadSize
		if err := s.Journal.Save(record); err != nil {
			return err
		}
		if record.BytesHave == record.Identity.ExpectedSize {
			record, err = s.prepareCommit(record, payloadPath)
			return err
		}
		return nil
	})
	return result(record), err
}

func (s PutService) Commit(ctx context.Context, clientID string, identity whale.Identity) (whale.PutResult, error) {
	if err := identity.Validate(); err != nil {
		return whale.PutResult{}, err
	}
	lockPath := filepath.Join(s.Journal.generationDir(identity.GenerationID), ".window.lock")
	var committed whale.PutResult
	err := repoworker.WithFileLock(lockPath, func() error {
		var err error
		committed, err = s.commitLocked(ctx, clientID, identity)
		return err
	})
	return committed, err
}

// commitLocked shares the generation lock with PUT_WINDOW. Consequently two
// retries cannot run svnmucc concurrently, and recovery may safely remove an
// abandoned transaction carrying this exact generation ID.
func (s PutService) commitLocked(ctx context.Context, clientID string, identity whale.Identity) (whale.PutResult, error) {
	access, err := s.authorize(ctx, clientID, identity.LogicalRepoID, true)
	if err != nil {
		return whale.PutResult{}, err
	}
	record, err := s.requireRecord(identity)
	if err != nil {
		return whale.PutResult{}, err
	}
	if record.State == whale.StatePublished {
		return result(record), s.cleanup(record.Identity)
	}
	if record.State.Terminal() {
		return result(record), errors.New("Whale PUT is terminal")
	}
	if _, _, _, err := s.Reservations.Reserve(ctx, record.Identity.GenerationID, record.Identity.ExpectedSize, s.now()); err != nil {
		return result(record), err
	}
	payloadPath, err := s.Journal.PayloadPath(record.Identity.GenerationID)
	if err != nil {
		return result(record), err
	}
	if record.State == whale.StateReceiving || record.State == whale.StateValidating {
		if record.BytesHave != record.Identity.ExpectedSize {
			return result(record), errors.New("Whale PUT is incomplete")
		}
		record, err = s.prepareCommit(record, payloadPath)
		if err != nil {
			return result(record), err
		}
	}
	if record.State != whale.StateCommitting {
		return result(record), errors.New("Whale PUT is not committable")
	}
	revision, err := s.Publisher.PublishWhale(ctx, &record, s.Journal, access.RepositoryPath, payloadPath)
	if err != nil {
		return result(record), err
	}
	record.Revision = revision
	record.State = whale.StatePublished
	if err := s.Journal.Save(record); err != nil {
		return result(record), err
	}
	return result(record), s.cleanup(record.Identity)
}

func (s PutService) Status(ctx context.Context, clientID string, identity whale.Identity) (whale.PutResult, error) {
	if _, err := s.authorize(ctx, clientID, identity.LogicalRepoID, false); err != nil {
		return whale.PutResult{}, err
	}
	record, err := s.requireRecord(identity)
	if err != nil {
		return whale.PutResult{}, err
	}
	return result(record), nil
}

func (s PutService) requireRecord(identity whale.Identity) (Record, error) {
	if err := identity.Validate(); err != nil {
		return Record{}, err
	}
	record, err := s.Journal.Load(identity.GenerationID)
	if err != nil {
		return Record{}, err
	}
	if record == nil {
		return Record{}, errors.New("Whale generation does not exist")
	}
	if record.Identity != identity || record.Direction != whale.DirectionPut {
		return Record{}, ErrGenerationConflict
	}
	return *record, nil
}

func (s PutService) prepareCommit(record Record, payloadPath string) (Record, error) {
	if record.State == whale.StateReceiving {
		record.State = whale.StateValidating
		if err := s.Journal.Save(record); err != nil {
			return record, err
		}
	}
	digest, size, err := digestFile(payloadPath)
	if err != nil {
		return record, err
	}
	if size != record.Identity.ExpectedSize || digest != record.Identity.SHA256 {
		record.State = whale.StateRejected
		if saveErr := s.Journal.Save(record); saveErr != nil {
			return record, saveErr
		}
		_ = s.Reservations.Release(record.Identity.GenerationID)
		_ = s.Queue.Release(record.Identity)
		return record, ErrDigestMismatch
	}
	record.State = whale.StateCommitting
	if err := s.Journal.Save(record); err != nil {
		return record, err
	}
	return record, nil
}

func (s PutService) cleanup(identity whale.Identity) error {
	reservationErr := s.Reservations.Release(identity.GenerationID)
	queueErr := s.Queue.Release(identity)
	return errors.Join(reservationErr, queueErr)
}

func (s PutService) authorize(ctx context.Context, clientID, repoID string, write bool) (RepositoryAccess, error) {
	if s.Authority == nil || s.Reservations == nil || s.Publisher == nil || !filepath.IsAbs(s.Queue.Root) {
		return RepositoryAccess{}, errors.New("Whale PUT service is incomplete")
	}
	access, err := s.Authority.ResolveWhale(ctx, clientID, repoID)
	if err != nil || (write && access.Access != "rw") || !filepath.IsAbs(access.RepositoryPath) {
		return RepositoryAccess{}, ErrAccessDenied
	}
	return access, nil
}

func (s PutService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func appendWindow(path string, offset, count int64, payload io.Reader) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < offset {
		return errors.New("Whale partial is missing or shorter than durable offset")
	}
	// Bytes written after the last durable journal update were never ACKed.
	// Discard them and let the client resend from the advertised OFFSET=N.
	if info.Size() > offset {
		if err := file.Truncate(offset); err != nil {
			return err
		}
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	written, err := io.CopyN(file, payload, count)
	if err != nil {
		return fmt.Errorf("Whale window ended after %d of %d bytes: %w", written, count, err)
	}
	return file.Sync()
}

func digestFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func result(record Record) whale.PutResult {
	return whale.PutResult{GenerationID: record.Identity.GenerationID, Offset: record.BytesHave, State: record.State, Revision: record.Revision}
}
