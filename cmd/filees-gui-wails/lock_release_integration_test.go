package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	"filees/pkg/client"
	"filees/pkg/clientview"
	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

// Opt-in acceptance uses two real IPC endpoints, the GUI runner/controller,
// durable worker records, private projections committed to SVN and real locks.
// Only the SSH control-plane delivery and native confirmation are replaced.
// It does not constitute acceptance of two standalone Wails windows or SSH.
func TestLockReleaseTwoEndpointsRealSVN(t *testing.T) {
	if os.Getenv("FILEES_TEST_LOCK_RELEASE_REAL") != "1" {
		t.Skip("set FILEES_TEST_LOCK_RELEASE_REAL=1 for isolated SVN acceptance")
	}
	if !repoworker.FileLocksSupported() {
		t.Skip("the server worker requires native kernel file locks")
	}
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Fatal(err)
	}
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"dismiss", "accept", "expired", "replacement"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			run := func(dir, tool string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, tool, args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "LC_ALL=C")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("lab command %s: %v: %s", filepath.Base(tool), err, out)
				}
				return string(out)
			}
			write := func(path, value string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			repoID := uuid.NewString()
			repository := filepath.Join(root, repoID)
			holderWC, requesterWC := filepath.Join(root, "holder"), filepath.Join(root, "requester")
			run(root, svnadmin, "create", repository)
			repoURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(repository)}).String()
			run(root, svn, "checkout", repoURL, holderWC)
			write(filepath.Join(holderWC, "file.txt"), "old")
			run(holderWC, svn, "add", "file.txt")
			run(holderWC, svn, "propset", "svn:needs-lock", "*", "file.txt")
			run(holderWC, svn, "commit", "-m", "lab seed")
			run(root, svn, "checkout", repoURL, requesterWC)
			repo := clientview.Repository{RepoID: repoID, DisplayName: "Lab", URL: "svn+ssh://_filees-client@lab.invalid/" + repoID, Access: "rw", State: "active"}
			holder := repoworker.Session{ClientID: uuid.NewString(), RealmID: uuid.NewString(), Repositories: []clientview.Repository{repo}}
			requester := repoworker.Session{ClientID: uuid.NewString(), RealmID: uuid.NewString(), Repositories: []clientview.Repository{repo}}
			run(holderWC, svn, "lock", "--username", holder.ClientID, "file.txt")
			write(filepath.Join(holderWC, "file.txt"), "new head")
			run(holderWC, svn, "commit", "--username", holder.ClientID, "--no-unlock", "-m", "lab holder update")

			serviceRepo, serviceWC := filepath.Join(root, "service-repo"), filepath.Join(root, "service")
			run(root, svnadmin, "create", serviceRepo)
			run(root, svn, "checkout", (&url.URL{Scheme: "file", Path: serviceRepo}).String(), serviceWC)
			outsider := repoworker.Session{ClientID: uuid.NewString(), RealmID: uuid.NewString()}
			for i, session := range []repoworker.Session{holder, requester, outsider} {
				view := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Lab", ClientID: session.ClientID, RealmID: session.RealmID,
					RealmAlias: fmt.Sprintf("lab%d", i), Generation: 1, GeneratedAt: time.Now(), ClientRole: "normal", Repositories: []clientview.Repository{repo}, ActiveOperations: []json.RawMessage{}}
				path := filepath.Join(serviceWC, "clients", session.ClientID, "view.json")
				if _, err := clientview.StoreIfNewer(path, view); err != nil {
					t.Fatal(err)
				}
			}
			run(serviceWC, svn, "add", "clients")
			run(serviceWC, svn, "commit", "-m", "lab private views")
			for _, wc := range []string{holderWC, requesterWC} {
				if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			helper := os.Getenv("FILEES_TEST_NATIVE_SPARSE")
			clientSVN := svn
			if helper != "" {
				// No silent fallback to the fixture CLI in native acceptance.
				clientSVN = filepath.Join(root, "absent-cli")
			}
			cli := client.New(client.Options{SvnPath: clientSVN, NativeSVNPath: helper, Timeout: 15 * time.Second})
			observe := func(wc string) client.LockObservation {
				t.Helper()
				got, err := cli.(client.LockObservationReader).ReadLockObservation(ctx, wc, filepath.Join(wc, "file.txt"))
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			initial := observe(holderWC)
			if initial.Remote == nil || initial.Local == nil {
				t.Fatal("holder lock is not present locally and remotely")
			}
			token := initial.Remote.Token
			clock := time.Now()
			store := &repoworker.FileLockReleaseStore{Root: filepath.Join(root, "requests"), Now: func() time.Time { return clock }}
			worker := &repoworker.Worker{LockReleases: store,
				LockAuthority: repoworker.SVNAdminLockAuthority{SVNAdmin: svnadmin, RepositoriesRoot: root},
				LockProjector: repoworker.ServicePublisher{ServiceWC: serviceWC, Runner: repoworker.SVNPublishRunner{SVN: svn, WorkingCopy: serviceWC}}}
			mu := &sync.Mutex{}
			holderServer, requesterServer := ipcserver.New(filepath.Join(root, "h.sock")), ipcserver.New(filepath.Join(root, "r.sock"))
			project := func() error {
				for _, endpoint := range []struct {
					session repoworker.Session
					server  *ipcserver.Server
				}{{holder, holderServer}, {requester, requesterServer}} {
					view, err := clientview.Load(filepath.Join(serviceWC, "clients", endpoint.session.ClientID, "view.json"))
					if err != nil {
						return err
					}
					var rows []contract.LockReleaseRequest
					for _, r := range view.LockReleaseRequests {
						rows = append(rows, contract.LockReleaseRequest{RequestID: r.RequestID, ServerID: "lab", RepoID: r.RepoID, Path: r.Path, ObservedLockID: r.ObservedLockID,
							Role: r.Role, State: r.State, CounterpartyRealmAlias: r.CounterpartyRealmAlias, CreatedAt: r.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: r.UpdatedAt.Format(time.RFC3339Nano), ExpiresAt: r.ExpiresAt.Format(time.RFC3339Nano)})
					}
					endpoint.server.SetLockReleaseProjection("lab", rows)
				}
				return nil
			}
			for _, endpoint := range []struct {
				session repoworker.Session
				server  *ipcserver.Server
				wc      string
			}{{holder, holderServer, holderWC}, {requester, requesterServer, requesterWC}} {
				wc := endpoint.wc
				endpoint.server.SetLockReleaseService(&lockReleaseLabTransport{worker: worker, session: endpoint.session, mu: mu, project: project})
				runtime := endpoint.server.RegisterRepoAccess(repoID, repoURL, wc, "lab", contract.AccessReadWrite)
				runtime.SetReservationFuncs(func(callCtx context.Context) (ipcserver.ReservationSnapshot, error) {
					obs, err := cli.(client.LockObservationReader).ReadLockObservation(callCtx, wc, filepath.Join(wc, "file.txt"))
					if err != nil {
						return ipcserver.ReservationSnapshot{}, err
					}
					if obs.Remote == nil {
						return ipcserver.ReservationSnapshot{}, nil
					}
					own := obs.Local != nil && obs.Local.Token == obs.Remote.Token
					return ipcserver.ReservationSnapshot{Reservations: []contract.Reservation{{RepoID: repoID, Path: "file.txt", Token: obs.Remote.Token, CanRelease: own}}}, nil
				}, func(callCtx context.Context, path, expected string, confirmed bool) error {
					obs, err := cli.(client.LockObservationReader).ReadLockObservation(callCtx, wc, filepath.Join(wc, path))
					if err != nil {
						return err
					}
					if !confirmed || obs.Local == nil || obs.Remote == nil || obs.Local.Token != expected || obs.Remote.Token != expected {
						return fmt.Errorf("lab fenced release refused")
					}
					_, err = cli.Unlock(callCtx, wc, []string{path})
					return err
				})
				if err := endpoint.server.Start(ctx); err != nil {
					t.Fatal(err)
				}
			}
			hIPC, rIPC := ipcclient.New(filepath.Join(root, "h.sock"), holder.ClientID), ipcclient.New(filepath.Join(root, "r.sock"), requester.ClientID)
			hGUI, rGUI := newGUIService(hIPC), newGUIService(rIPC)
			go hGUI.runner.Run(ctx)
			go rGUI.runner.Run(ctx)
			wait := func(label string, ready func() bool) {
				t.Helper()
				deadline := time.Now().Add(8 * time.Second)
				for time.Now().Before(deadline) {
					if ready() {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Fatalf("timeout: %s", label)
			}
			wait("both GUI clients connected", func() bool { return hGUI.viewModel().Connected && rGUI.viewModel().Connected })
			request, err := rIPC.LockReleaseRequest(ctx, contract.LockReleaseRequestPayload{ServerID: "lab", RepoID: repoID, Path: "file.txt", ObservedLockID: token})
			if err != nil {
				t.Fatal(err)
			}
			retry, err := rIPC.LockReleaseRequest(ctx, contract.LockReleaseRequestPayload{ServerID: "lab", RepoID: repoID, Path: "file.txt", ObservedLockID: token})
			if err != nil || retry.RequestID != request.RequestID {
				t.Fatal("repeated request lost its identity", retry, err)
			}
			wait("both private GUI projections", func() bool {
				return len(hGUI.Snapshot().LockReleaseRequests) == 1 && len(rGUI.Snapshot().LockReleaseRequests) == 1
			})
			hRow, rRow := hGUI.Snapshot().LockReleaseRequests[0], rGUI.Snapshot().LockReleaseRequests[0]
			if !hRow.CanAccept || !hRow.CanDismiss || rRow.CanAccept || rRow.CanDismiss || hRow.CounterpartyRealmAlias != "lab1" {
				t.Fatalf("role projection: holder=%+v requester=%+v", hRow, rRow)
			}
			for _, gui := range []*GUIService{hGUI, rGUI} {
				raw, err := json.Marshal(gui.Snapshot())
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), token) || strings.Contains(string(raw), holder.ClientID) || strings.Contains(string(raw), requester.ClientID) {
					t.Fatal("renderer leaked lock token or installation identity")
				}
			}
			view, err := clientview.Load(filepath.Join(serviceWC, "clients", outsider.ClientID, "view.json"))
			if err != nil || len(view.LockReleaseRequests) != 0 {
				t.Fatal("request reached unrelated private view", err)
			}
			// A fresh GUI reconstructs pending state from IPC, not renderer memory.
			restarted := newGUIService(hIPC)
			restartCtx, restartCancel := context.WithCancel(ctx)
			go restarted.runner.Run(restartCtx)
			wait("pending after GUI restart", func() bool {
				return len(restarted.Snapshot().LockReleaseRequests) == 1 && restarted.Snapshot().LockReleaseRequests[0].CanAccept
			})
			restartCancel()
			if obs := observe(holderWC); obs.Remote == nil || obs.Remote.Token != token {
				t.Fatal("request/reconnect changed lock")
			}

			if scenario == "expired" {
				mu.Lock()
				clock = clock.Add(4 * time.Hour)
				mu.Unlock()
			}
			if scenario == "replacement" {
				run(holderWC, svn, "unlock", "--username", holder.ClientID, "file.txt")
				run(holderWC, svn, "lock", "--username", holder.ClientID, "file.txt")
			}
			if scenario == "expired" || scenario == "replacement" {
				before := observe(holderWC).Remote.Token
				result, err := hIPC.LockReleaseAccept(ctx, contract.LockReleaseDecisionPayload{ServerID: "lab", RequestID: request.RequestID})
				if scenario == "replacement" && err == nil {
					t.Fatal("stale request accepted", result)
				}
				if scenario == "expired" && (err != nil || result.State != "expired") {
					t.Fatal("expiry not projected", result, err)
				}
				if obs := observe(holderWC); obs.Remote == nil || obs.Remote.Token != before {
					t.Fatal("expired/stale request unlocked current lock")
				}
				return
			}
			intents := make(chan tray.Intent, 4)
			hGUI.attachActions(intents)
			confirmations, answers, notifications := make(chan platform.ConfirmRequest, 2), make(chan bool, 2), make(chan platform.Notification, 4)
			fake := &platformtest.Fake{ConfirmFunc: func(_ context.Context, r platform.ConfirmRequest) (bool, error) {
				confirmations <- r
				select {
				case answer := <-answers:
					return answer, nil
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}, NotifyFunc: func(_ context.Context, n platform.Notification) error { notifications <- n; return nil }}
			controllerConfig := actions.Config{Intents: intents, ViewModel: hGUI.viewModel, Prompter: fake, Notifier: fake, LockReleases: lockReleaseAdapter{client: hIPC}, Refresh: hGUI.runner.Refresh}
			controllerCtx, controllerCancel := context.WithCancel(ctx)
			defer controllerCancel()
			controllerDone := make(chan struct{})
			go func() { actions.New(controllerConfig).Run(controllerCtx); close(controllerDone) }()
			trigger := func(kind tray.IntentKind) {
				t.Helper()
				if result := hGUI.Trigger(ActionRequest{Kind: string(kind), LockReleaseRequestID: request.RequestID}); !result.Accepted {
					t.Fatal("GUI refused action", result)
				}
			}
			if scenario == "dismiss" {
				trigger(tray.IntentDismissLockRelease)
				wait("dismissed projection", func() bool { return hGUI.Snapshot().LockReleaseRequests[0].State == "dismissed" })
				if obs := observe(holderWC); obs.Remote == nil || obs.Remote.Token != token {
					t.Fatal("OK/dismiss removed lock")
				}
				return
			}
			trigger(tray.IntentAcceptLockRelease)
			select {
			case <-confirmations:
			case <-ctx.Done():
				t.Fatal("no confirmation")
			}
			answers <- false
			// Cancellation intentionally emits no decision notification. Join the
			// controller instead of treating silence as evidence of completion.
			controllerCancel()
			select {
			case <-controllerDone:
			case <-ctx.Done():
				t.Fatal("cancel did not finish")
			}
			if obs := observe(holderWC); obs.Remote == nil || obs.Remote.Token != token {
				t.Fatal("cancel removed lock")
			}
			if hGUI.Snapshot().LockReleaseRequests[0].State != "pending" {
				t.Fatal("cancel became a server decision")
			}
			go actions.New(controllerConfig).Run(ctx)
			trigger(tray.IntentAcceptLockRelease)
			select {
			case <-confirmations:
			case <-ctx.Done():
				t.Fatal("no second confirmation")
			}
			answers <- true
			wait("holder unlock", func() bool { return observe(holderWC).Remote == nil })
			wait("accepted requester projection", func() bool { return rGUI.Snapshot().LockReleaseRequests[0].State == "accepted" })
			if err := os.MkdirAll(filepath.Join(requesterWC, ".filees", "state"), 0700); err != nil {
				t.Fatal(err)
			}
			claim := &commit.Service{Cli: cli, RepoURL: repoURL}
			accepted := requesterServer.LockReleaseRequests("lab", repoID)[0]
			calls := 0
			acquire := func(c context.Context, paths []string) (string, error) {
				calls++
				return cli.Lock(c, requesterWC, paths)
			}
			if done, err := claim.ClaimReleasedLock(ctx, repoID, requesterWC, accepted, acquire); !done || err != nil {
				t.Fatal("claim failed", done, err)
			}
			raw, err := os.ReadFile(filepath.Join(requesterWC, "file.txt"))
			if err != nil || string(raw) != "new head" {
				t.Fatal("claim did not update to holder HEAD", err)
			}
			obs := observe(requesterWC)
			if obs.Local == nil || obs.Remote == nil || obs.Local.Token != obs.Remote.Token || obs.Remote.Token == token {
				t.Fatal("requester has no new matching lock")
			}
			if _, err := cli.Unlock(ctx, requesterWC, []string{"file.txt"}); err != nil {
				t.Fatal(err)
			}
			claim = &commit.Service{Cli: cli, RepoURL: repoURL}
			if done, err := claim.ClaimReleasedLock(ctx, repoID, requesterWC, accepted, acquire); !done || err != nil || calls != 1 {
				t.Fatal("consent replayed after restart", done, err, calls)
			}
			if observe(requesterWC).Remote != nil {
				t.Fatal("reacquired after manual release")
			}
		})
	}
}

// In-process delivery replaces authenticated SSH only. The worker derives
// identity from this fixed lab session; the payload cannot choose an actor.
type lockReleaseLabTransport struct {
	worker  *repoworker.Worker
	session repoworker.Session
	mu      *sync.Mutex
	project func() error
}

func (a *lockReleaseLabTransport) call(ctx context.Context, typ control.TicketType, payload any) (contract.LockReleaseRequest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), typ, a.session.ClientID, payload, time.Now())
	if err != nil {
		return contract.LockReleaseRequest{}, err
	}
	result, err := a.worker.Handle(ctx, a.session, ticket)
	if err != nil {
		return contract.LockReleaseRequest{}, err
	}
	if result.Status != control.ResultOK {
		return contract.LockReleaseRequest{}, fmt.Errorf("lab worker: %+v", result.Error)
	}
	var row control.LockReleaseResult
	if err := control.DecodePayload(result.Result, &row); err != nil {
		return contract.LockReleaseRequest{}, err
	}
	if err := a.project(); err != nil {
		return contract.LockReleaseRequest{}, err
	}
	return contract.LockReleaseRequest{RequestID: row.RequestID, ServerID: "lab", RepoID: row.RepoID, Path: row.Path, ObservedLockID: row.ObservedLockID, State: row.State, ExpiresAt: row.ExpiresAt}, nil
}
func (a *lockReleaseLabTransport) Request(ctx context.Context, p contract.LockReleaseRequestPayload) (contract.LockReleaseRequest, error) {
	return a.call(ctx, control.TicketRequestLockRelease, control.RequestLockReleasePayload{RepoID: p.RepoID, Path: p.Path, ObservedLockID: p.ObservedLockID})
}
func (a *lockReleaseLabTransport) Dismiss(ctx context.Context, p contract.LockReleaseDecisionPayload) (contract.LockReleaseRequest, error) {
	return a.call(ctx, control.TicketDismissLockRelease, control.DecideLockReleasePayload{RequestID: p.RequestID})
}
func (a *lockReleaseLabTransport) Accept(ctx context.Context, p contract.LockReleaseDecisionPayload) (contract.LockReleaseRequest, error) {
	return a.call(ctx, control.TicketAcceptLockRelease, control.DecideLockReleasePayload{RequestID: p.RequestID})
}
