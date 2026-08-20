package whaleworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/pkg/repoworker"
	whale "filees/pkg/whale/v1"
	"github.com/google/uuid"
)

type putAuthority struct {
	repo   string
	access string
}

type blockingPutPublisher struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (p *blockingPutPublisher) PublishWhale(_ context.Context, record *Record, _ Journal, _, _ string) (int64, error) {
	p.mu.Lock()
	p.calls++
	if p.calls == 1 {
		close(p.started)
	}
	p.mu.Unlock()
	<-p.release
	record.CommitBaseKnown = true
	return 91, nil
}

func (p *blockingPutPublisher) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (a putAuthority) ResolveWhale(context.Context, string, string) (RepositoryAccess, error) {
	if a.access == "" {
		return RepositoryAccess{}, ErrAccessDenied
	}
	return RepositoryAccess{RepositoryPath: a.repo, Access: a.access}, nil
}

type putReservations struct {
	reserved int
	released int
}

type insufficientReservations struct{}

func (insufficientReservations) Reserve(context.Context, string, int64, time.Time) (int64, int64, time.Time, error) {
	return 89, 180, time.Time{}, repoworker.ErrReservationUnavailable
}
func (insufficientReservations) Ensure(context.Context, string, time.Time) error { return nil }
func (insufficientReservations) Release(string) error                            { return nil }

func (r *putReservations) Reserve(context.Context, string, int64, time.Time) (int64, int64, time.Time, error) {
	r.reserved++
	return 1 << 40, 1, time.Now().Add(time.Hour), nil
}
func (*putReservations) Ensure(context.Context, string, time.Time) error { return nil }
func (r *putReservations) Release(string) error {
	r.released++
	return nil
}

func TestPutReservationPreservesCapacityNumbers(t *testing.T) {
	service := PutService{Reservations: insufficientReservations{}}
	err := service.reserve(context.Background(), uuid.NewString(), 100)
	var capacity InsufficientSpaceError
	if !errors.As(err, &capacity) || capacity.AvailableBytes != 89 || capacity.RequiredBytes != 180 {
		t.Fatalf("capacity=%+v err=%v", capacity, err)
	}
}

type putPublisher struct {
	calls int
	rev   int64
}

func (p *putPublisher) PublishWhale(_ context.Context, record *Record, _ Journal, repositoryPath, payloadPath string) (int64, error) {
	p.calls++
	record.CommitBaseKnown = true
	if !filepath.IsAbs(repositoryPath) || record.State != whale.StateCommitting {
		return 0, errors.New("bad publish input")
	}
	if raw, err := os.ReadFile(payloadPath); err != nil || string(raw) != "abcdef" {
		return 0, errors.New("bad publish payload")
	}
	return p.rev, nil
}

func putIdentity(payload []byte) whale.Identity {
	digest := sha256.Sum256(payload)
	return whale.Identity{
		LogicalRepoID: uuid.NewString(), LogicalPath: "media/video.bin", GenerationID: uuid.NewString(),
		ExpectedSize: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
	}
}

func putRequest(identity whale.Identity, offset int64, payload []byte) whale.Request {
	return whale.Request{Schema: whale.Schema, RequestID: uuid.NewString(), Operation: whale.OpPutWindow, Identity: identity, Offset: offset, PayloadSize: int64(len(payload))}
}

func TestPutWindowsAckOnlyDurableOffsetThenPublishOnce(t *testing.T) {
	repo := t.TempDir()
	reservations := &putReservations{}
	publisher := &putPublisher{rev: 184}
	stateRoot := t.TempDir()
	service := PutService{Journal: Journal{Root: filepath.Join(stateRoot, "journal")}, Queue: PathQueue{Root: filepath.Join(stateRoot, "queues")}, Authority: putAuthority{repo: repo, access: "rw"}, Reservations: reservations, Publisher: publisher}
	identity := putIdentity([]byte("abcdef"))
	first, err := service.ReceiveWindow(context.Background(), "client", putRequest(identity, 0, []byte("abc")), bytes.NewReader([]byte("abc")))
	if err != nil || first.Offset != 3 || first.State != whale.StateReceiving {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := service.ReceiveWindow(context.Background(), "client", putRequest(identity, 3, []byte("def")), bytes.NewReader([]byte("def")))
	if err != nil || second.Offset != 6 || second.State != whale.StateCommitting {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	published, err := service.Commit(context.Background(), "client", identity)
	if err != nil || published.State != whale.StatePublished || published.Revision != 184 || publisher.calls != 1 || reservations.released != 1 {
		t.Fatalf("published=%+v calls=%d reserve=%d release=%d err=%v", published, publisher.calls, reservations.reserved, reservations.released, err)
	}
	again, err := service.Commit(context.Background(), "client", identity)
	if err != nil || again != published || publisher.calls != 1 {
		t.Fatalf("idempotent commit=%+v calls=%d err=%v", again, publisher.calls, err)
	}
}

func TestConcurrentCommitRetriesCannotPublishGenerationTwice(t *testing.T) {
	stateRoot := t.TempDir()
	publisher := &blockingPutPublisher{started: make(chan struct{}), release: make(chan struct{})}
	service := PutService{
		Journal: Journal{Root: filepath.Join(stateRoot, "journal")}, Queue: PathQueue{Root: filepath.Join(stateRoot, "queues")},
		Authority: putAuthority{repo: t.TempDir(), access: "rw"}, Reservations: &putReservations{}, Publisher: publisher,
	}
	identity := putIdentity([]byte("abcdef"))
	if _, err := service.ReceiveWindow(context.Background(), "client", putRequest(identity, 0, []byte("abcdef")), bytes.NewReader([]byte("abcdef"))); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result whale.PutResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := service.Commit(context.Background(), "client", identity)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	select {
	case <-publisher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first commit did not reach publisher")
	}
	time.Sleep(100 * time.Millisecond)
	if calls := publisher.callCount(); calls != 1 {
		t.Fatalf("concurrent commit entered publisher %d times", calls)
	}
	close(publisher.release)
	published, retry := 0, 0
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			if !strings.Contains(got.err.Error(), "already active") {
				t.Fatalf("unexpected concurrent commit error: %v", got.err)
			}
			retry++
			continue
		}
		if got.result.State != whale.StatePublished || got.result.Revision != 91 {
			t.Fatalf("commit outcome=%+v err=%v", got.result, got.err)
		}
		published++
	}
	if calls := publisher.callCount(); calls != 1 {
		t.Fatalf("serialized retry published %d times", calls)
	}
	if published < 1 || published+retry != 2 {
		t.Fatalf("published=%d retry=%d, want two resolved callers", published, retry)
	}
}

func TestPutRejectsWrongOffsetWithoutChangingPartial(t *testing.T) {
	repo := t.TempDir()
	stateRoot := t.TempDir()
	service := PutService{Journal: Journal{Root: filepath.Join(stateRoot, "journal")}, Queue: PathQueue{Root: filepath.Join(stateRoot, "queues")}, Authority: putAuthority{repo: repo, access: "rw"}, Reservations: &putReservations{}, Publisher: &putPublisher{rev: 1}}
	identity := putIdentity([]byte("abcdef"))
	if _, err := service.ReceiveWindow(context.Background(), "client", putRequest(identity, 0, []byte("abc")), bytes.NewReader([]byte("abc"))); err != nil {
		t.Fatal(err)
	}
	result, err := service.ReceiveWindow(context.Background(), "client", putRequest(identity, 2, []byte("def")), bytes.NewReader([]byte("def")))
	if !errors.Is(err, ErrOffsetConflict) || result.Offset != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	path, _ := service.Journal.PayloadPath(identity.GenerationID)
	if raw, _ := os.ReadFile(path); string(raw) != "abc" {
		t.Fatalf("partial=%q", raw)
	}
}

func TestUnackedTailIsTruncatedAndResentFromOffsetN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.partial")
	if err := os.WriteFile(path, []byte("abcSTALE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendWindow(path, 3, 3, bytes.NewReader([]byte("def"))); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "abcdef" {
		t.Fatalf("partial=%q", raw)
	}
}

func TestPutRejectsDigestMismatchAndReadOnlyClient(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	identity := putIdentity([]byte("good"))
	reservations := &putReservations{}
	service := PutService{Journal: Journal{Root: root}, Queue: PathQueue{Root: filepath.Join(filepath.Dir(root), "queues")}, Authority: putAuthority{repo: t.TempDir(), access: "rw"}, Reservations: reservations, Publisher: &putPublisher{rev: 1}}
	result, err := service.ReceiveWindow(context.Background(), "client", putRequest(identity, 0, []byte("evil")), bytes.NewReader([]byte("evil")))
	if !errors.Is(err, ErrDigestMismatch) || result.State != whale.StateRejected || reservations.released != 1 {
		t.Fatalf("result=%+v released=%d err=%v", result, reservations.released, err)
	}
	service.Authority = putAuthority{repo: t.TempDir(), access: "r"}
	other := putIdentity([]byte("x"))
	if _, err := service.ReceiveWindow(context.Background(), "client", putRequest(other, 0, []byte("x")), bytes.NewReader([]byte("x"))); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("readonly err=%v", err)
	}
}

func TestShortWindowIsNeverAcknowledged(t *testing.T) {
	stateRoot := t.TempDir()
	service := PutService{Journal: Journal{Root: filepath.Join(stateRoot, "journal")}, Queue: PathQueue{Root: filepath.Join(stateRoot, "queues")}, Authority: putAuthority{repo: t.TempDir(), access: "rw"}, Reservations: &putReservations{}, Publisher: &putPublisher{rev: 1}}
	identity := putIdentity([]byte("abcd"))
	request := putRequest(identity, 0, []byte("abcd"))
	if _, err := service.ReceiveWindow(context.Background(), "client", request, bytes.NewReader([]byte("ab"))); err == nil {
		t.Fatal("short window accepted")
	}
	status, err := service.Status(context.Background(), "client", identity)
	if err != nil || status.Offset != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}
