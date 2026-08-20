package whaleworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
)

type memoryGetSource struct {
	content      []byte
	quotes       int
	materialized int
}

func (s *memoryGetSource) Discover(_ context.Context, _ string, repoID, logicalPath string, _ int64) (whale.Identity, int64, error) {
	digest := sha256.Sum256(s.content)
	return whale.Identity{LogicalRepoID: repoID, LogicalPath: logicalPath, GenerationID: uuid.NewString(), ExpectedSize: int64(len(s.content)), SHA256: hex.EncodeToString(digest[:])}, 7, nil
}

func (s *memoryGetSource) Quote(_ context.Context, _ string, identity whale.Identity, _ int64) error {
	s.quotes++
	if int64(len(s.content)) != identity.ExpectedSize {
		return errors.New("wrong size")
	}
	return nil
}

func (s *memoryGetSource) Materialize(_ context.Context, _ string, _ whale.Identity, _ int64, destination string) error {
	s.materialized++
	return os.WriteFile(destination, s.content, 0o600)
}

func getRequest(operation whale.Operation, identity whale.Identity) whale.Request {
	request := whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: operation, Identity: identity, Revision: 7}
	if operation == whale.OpGetWindow || operation == whale.OpGetRelease {
		request.TransferID = uuid.NewString()
		request.ConfirmationToken = uuid.NewString()
	}
	return request
}

func TestGetQuoteDoesNotReserveOrMaterialize(t *testing.T) {
	content := []byte("0123456789")
	identity := putIdentity(content)
	source := &memoryGetSource{content: content}
	reservations := &putReservations{}
	service := GetService{Root: filepath.Join(t.TempDir(), "cache"), Authority: putAuthority{repo: t.TempDir(), access: "r"}, Reservations: reservations, Source: source}
	result, err := service.Quote(context.Background(), "client", getRequest(whale.OpGetQuote, identity))
	if err != nil {
		t.Fatal(err)
	}
	if result.State != whale.StateAwaitingConfirmation || source.quotes != 1 || source.materialized != 0 || reservations.reserved != 0 {
		t.Fatalf("result=%+v source=%+v reservations=%+v", result, source, reservations)
	}
	if _, err := os.Stat(service.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quote created cache state: %v", err)
	}
}

func TestGetWindowMaterializesOnceAndResumesAtExactOffset(t *testing.T) {
	content := []byte("0123456789")
	identity := putIdentity(content)
	source := &memoryGetSource{content: content}
	reservations := &putReservations{}
	service := GetService{Root: filepath.Join(t.TempDir(), "cache"), Authority: putAuthority{repo: t.TempDir(), access: "rw"}, Reservations: reservations, Source: source}
	request := getRequest(whale.OpGetWindow, identity)
	request.Offset, request.PayloadSize = 3, 4
	var first bytes.Buffer
	result, err := service.ServeWindow(context.Background(), "client", request, func(result whale.Result, payload io.Reader) error {
		_, err := io.Copy(&first, payload)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.String() != "3456" || result.Offset != 3 || result.PayloadSize != 4 {
		t.Fatalf("payload=%q result=%+v", first.String(), result)
	}

	request.Offset, request.PayloadSize = 7, 3
	var resumed bytes.Buffer
	if _, err := service.ServeWindow(context.Background(), "client", request, func(_ whale.Result, payload io.Reader) error {
		_, err := io.Copy(&resumed, payload)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if resumed.String() != "789" || source.materialized != 1 {
		t.Fatalf("resume=%q materializations=%d", resumed.String(), source.materialized)
	}
}

func TestGetTransferCannotBeReboundAndReleaseIsIdempotent(t *testing.T) {
	content := []byte("abcdef")
	identity := putIdentity(content)
	source := &memoryGetSource{content: content}
	reservations := &putReservations{}
	service := GetService{Root: filepath.Join(t.TempDir(), "cache"), Authority: putAuthority{repo: t.TempDir(), access: "r"}, Reservations: reservations, Source: source}
	request := getRequest(whale.OpGetWindow, identity)
	request.PayloadSize = identity.ExpectedSize
	if _, err := service.ServeWindow(context.Background(), "client", request, func(_ whale.Result, payload io.Reader) error {
		_, err := io.Copy(io.Discard, payload)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rebound := request
	rebound.ConfirmationToken = uuid.NewString()
	if _, err := service.ServeWindow(context.Background(), "client", rebound, func(_ whale.Result, _ io.Reader) error { return nil }); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("rebind error = %v", err)
	}
	release := request
	release.Operation = whale.OpGetRelease
	release.Offset, release.PayloadSize = 0, 0
	if _, err := service.Release(context.Background(), "client", release); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Release(context.Background(), "client", release); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	if _, err := os.Stat(service.transferDir(request.TransferID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache survived release: %v", err)
	}
}

func TestGetAdmissionPrunesExpiredSeekableCache(t *testing.T) {
	content := []byte("abcdef")
	identity := putIdentity(content)
	source := &memoryGetSource{content: content}
	reservations := &putReservations{}
	now := time.Now().UTC()
	service := GetService{Root: filepath.Join(t.TempDir(), "cache"), Authority: putAuthority{repo: t.TempDir(), access: "r"}, Reservations: reservations, Source: source, Now: func() time.Time { return now }}
	request := getRequest(whale.OpGetWindow, identity)
	request.PayloadSize = identity.ExpectedSize
	if _, err := service.ServeWindow(context.Background(), "client", request, func(_ whale.Result, payload io.Reader) error {
		_, err := io.Copy(io.Discard, payload)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.payloadPath(request.TransferID)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := service.Quote(context.Background(), "client", getRequest(whale.OpGetQuote, identity)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.transferDir(request.TransferID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired cache survived: %v", err)
	}
}
