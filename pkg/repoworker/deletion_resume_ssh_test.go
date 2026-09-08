package repoworker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"github.com/google/uuid"
)

type deletionResumeEffects struct {
	ServerEffects
	repo, realm   string
	archives      atomic.Int32
	beforeArchive func()
}

// Only the authority publisher is a fixture. The durable backend, result
// store, SSH pins, svnadmin dump/load/verify and source removal are real.
func (e *deletionResumeEffects) AuthorizeDelete(_ context.Context, repo, realm string) error {
	if repo != e.repo || realm != e.realm {
		return errors.New("fixture ownership mismatch")
	}
	return nil
}

func (e *deletionResumeEffects) WithdrawAuthority(ctx context.Context, repo, realm string) error {
	return e.AuthorizeDelete(ctx, repo, realm)
}

func (e *deletionResumeEffects) ArchiveAndDeleteFSFS(ctx context.Context, repo, op string) (time.Time, error) {
	e.archives.Add(1)
	if e.beforeArchive != nil {
		e.beforeArchive()
	}
	return e.ServerEffects.ArchiveAndDeleteFSFS(ctx, repo, op)
}

func TestDeletionResumePinnedSSHDoesNotRepeatArchive(t *testing.T) {
	for _, mode := range []string{"lost-during-archive", "lost-reply", "lost-exit-status", "wrong-host-pin"} {
		t.Run(mode, func(t *testing.T) {
			e, repoID, repo := deletionFixture(t, 7, time.Now())
			session := Session{ClientID: uuid.NewString(), RealmID: uuid.NewString(), CanCreateRepositories: true}
			fx := &deletionResumeEffects{ServerEffects: e, repo: repoID, realm: session.RealmID}
			connections := make(chan io.Closer, 1)
			if mode == "lost-during-archive" {
				fx.beforeArchive = func() { _ = (<-connections).Close() }
			}
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			worker := &Worker{Backend: &DurableBackend{Root: t.TempDir(), Effects: fx}, Store: store}
			dispatcher := Dispatcher{Worker: worker, Resolver: preparationResolver{session}}
			client, cfg, handled := controlSSHFixture(t, dispatcher, session.ClientID,
				func(n int32) bool { return mode == "lost-reply" && n == 1 },
				func(n int32) bool { return mode == "lost-exit-status" && n == 1 },
				func(connection io.Closer) {
					select {
					case connections <- connection:
					default:
					}
				})
			if mode == "wrong-host-pin" {
				cfg.KnownHosts = filepath.Join(t.TempDir(), "empty-pins")
				if err := os.WriteFile(cfg.KnownHosts, nil, 0600); err != nil {
					t.Fatal(err)
				}
				client, err = controlclient.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketDeleteRepository, session.ClientID, control.DeleteRepositoryPayload{RepoID: repoID}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			result, err := controlclient.ResumeRepositoryDeletion(ctx, client, ticket)
			if mode == "wrong-host-pin" {
				if err == nil || handled.Load() != 0 || fx.archives.Load() != 0 || !validRepo(repo) {
					t.Fatalf("pin rejection err=%v requests=%d dumps=%d", err, handled.Load(), fx.archives.Load())
				}
				return
			}
			if err != nil || result.Status != control.ResultOK || fx.archives.Load() != 1 {
				t.Fatalf("result=%+v err=%v dumps=%d", result, err, fx.archives.Load())
			}
			want := int32(1)
			if mode == "lost-reply" || mode == "lost-during-archive" {
				want = 2
			}
			if handled.Load() != want {
				t.Fatalf("requests=%d want=%d", handled.Load(), want)
			}
			if _, err := os.Stat(repo); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source still exists: %v", err)
			}
			if _, _, found, err := DeletionRecoveryArchive(e.DeletionArchiveRoot, repoID, ticket.OperationID); err != nil || !found {
				t.Fatalf("archive found=%v err=%v", found, err)
			}
		})
	}
}
