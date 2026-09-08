package controlclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func deleteResumeTicket(t *testing.T) control.Ticket {
	t.Helper()
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketDeleteRepository, uuid.NewString(), control.DeleteRepositoryPayload{RepoID: uuid.NewString()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestDeletionResumeKeepsExactTicketAndBacksOff(t *testing.T) {
	ticket := deleteResumeTicket(t)
	before, _ := json.Marshal(ticket)
	calls, waits := 0, []time.Duration{}
	result, _ := control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{Code: "DELETE_REPOSITORY_RETRY", Message: "capacity guard"}, time.Now())
	got, err := resumeRepositoryDeletion(t.Context(), passportExchange(func(_ context.Context, actual control.Ticket) (control.Result, error) {
		calls++
		raw, _ := json.Marshal(actual)
		if string(raw) != string(before) {
			t.Fatal("retry minted/changed a ticket")
		}
		if calls < 8 {
			return control.Result{}, io.ErrUnexpectedEOF
		}
		return result, nil
	}), ticket, func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	})
	if err != nil || got.Status != control.ResultError || calls != 8 {
		t.Fatalf("result=%+v err=%v calls=%d", got, err, calls)
	}
	want := []time.Duration{2, 4, 8, 16, 30, 30, 30}
	for i, delay := range waits {
		if delay != want[i]*time.Second {
			t.Fatalf("backoff=%v", waits)
		}
	}
}

func TestDeletionResumeDoesNotRetryRejectionOrBadProof(t *testing.T) {
	for _, mode := range []string{"rejected", "wrong-request", "wrong-repo", "malformed", "host-pin", "remote-exit"} {
		t.Run(mode, func(t *testing.T) {
			ticket := deleteResumeTicket(t)
			result, _ := control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{Code: "DELETE_REPOSITORY_RETRY", Message: "capacity guard"}, time.Now())
			_, err := resumeRepositoryDeletion(t.Context(), passportExchange(func(context.Context, control.Ticket) (control.Result, error) {
				switch mode {
				case "wrong-repo":
					result, _ = control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.DeleteRepositoryResult{RepoID: uuid.NewString(), RetainUntil: time.Now().Format(time.RFC3339Nano)}, time.Now())
				case "wrong-request":
					result.RequestID = uuid.NewString()
				case "malformed":
					result.Schema = "bad"
				case "host-pin":
					return control.Result{}, errors.New("host key mismatch")
				case "remote-exit":
					return control.Result{}, &ssh.ExitError{}
				}
				return result, nil
			}), ticket, func(context.Context, time.Duration) error { t.Fatal("must not retry"); return nil })
			if (err == nil) != (mode == "rejected") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDeletionResumeCancellationStopsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err := resumeRepositoryDeletion(ctx, passportExchange(func(context.Context, control.Ticket) (control.Result, error) {
		calls++
		return control.Result{}, io.EOF
	}), deleteResumeTicket(t), func(ctx context.Context, delay time.Duration) error {
		cancel()
		return waitDeletionReconnect(ctx, delay)
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDeletionRecoveryResumeKeepsKeyAndBindsManifest(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewPublicKey(public)
	for _, mode := range []string{"ok", "other-operation", "other-repository"} {
		t.Run(mode, func(t *testing.T) {
			repo, op := uuid.NewString(), uuid.NewString()
			ticket, err := control.NewTicket(op, uuid.NewString(), control.TicketPrepareRepositoryRecovery, uuid.NewString(), control.PrepareRepositoryRecoveryPayload{RepoID: repo, RecoveryPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			manifest := control.RealmRecoveryManifest{Schema: "filees.realm-recovery-manifest/v1", OperationID: op, RealmID: uuid.NewString(), CreatedAt: time.Now(), DownloadUntil: time.Now().Add(time.Hour), AdminGraceUntil: time.Now().Add(2 * time.Hour), Archives: []control.RealmRecoveryArchive{{ArchiveID: uuid.NewString(), RepoID: repo, SHA256: strings.Repeat("a", 64), Size: 1}}}
			if mode == "other-operation" {
				manifest.OperationID = uuid.NewString()
			}
			if mode == "other-repository" {
				manifest.Archives[0].RepoID = uuid.NewString()
			}
			result, err := control.NewSuccessResult(op, ticket.RequestID, ticket.Type, control.PrepareRepositoryRecoveryResult{Manifest: manifest}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err = resumeRepositoryDeletion(t.Context(), passportExchange(func(_ context.Context, got control.Ticket) (control.Result, error) {
				calls++
				if string(got.Payload) != string(ticket.Payload) || got.RequestID != ticket.RequestID {
					t.Fatal("recovery retry changed key or identity")
				}
				if calls == 1 {
					return control.Result{}, io.EOF
				}
				return result, nil
			}), ticket, func(context.Context, time.Duration) error { return nil })
			if (err == nil) != (mode == "ok") || calls != 2 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
