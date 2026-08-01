// Package ipcserver implements the FileES daemon-side IPC contract server.
// It listens on a Unix domain socket, speaks filees.contract/v1 over JSON Lines,
// and is the single point of contact between the daemon engine and all clients.
package ipcserver

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"filees/pkg/activity"
	contract "filees/pkg/contract/v1"
	"filees/pkg/talk"
)

// Server is the IPC contract server. Create with New, register repos with
// RegisterRepo, then call Start. Safe for concurrent use.
type Server struct {
	sockPath  string
	startTime time.Time
	lg        talk.Logger

	mu           sync.RWMutex
	repos        map[string]*RepoState // keyed by repo ID
	activations  map[string]contract.ActivationStatus
	activation   ActivationService
	realmAlias   RealmAliasService
	ownerLabels  OwnerLabelResolver
	lifecycle    RepositoryLifecycleService
	mobilePair   MobilePairingService
	serverDetach ServerDetachService
	realmRemoval RealmRemovalService
	updates      UpdateService
	activity     ActivitySource
	lifecycleFn  SystemLifecycleService

	connsMu  sync.Mutex
	conns    map[net.Conn]struct{}
	stopping bool

	evSeq int64 // atomic monotone counter for Event.Sequence

	subsMu sync.Mutex
	subs   map[chan contract.Event]struct{}
}

type ActivationService interface {
	Begin(context.Context, contract.ActivationBeginPayload) (contract.ActivationCommandResult, error)
	Finish(context.Context, contract.ActivationFinishPayload) (contract.ActivationCommandResult, error)
}

type RealmAliasService interface {
	Claim(context.Context, string, string) (string, error)
}

// OwnerLabelResolver converts opaque SVN client IDs to server-owned display
// aliases. It is deliberately absent from GUI-facing contracts.
type OwnerLabelResolver interface {
	Resolve(context.Context, string, []string) (map[string]string, error)
}

type RepositoryLifecycleService interface {
	BeginCreate(serverID, displayName, localPath string) (contract.RepoLifecycleResult, error)
	BeginAttach(serverID, repoID, localPath string, required bool) (contract.RepoLifecycleResult, error)
	ApproveAttach(operationID, serverID, repoID, repoURL, access string) (contract.RepoLifecycleResult, error)
	BeginRelocate(serverID, repoID, newLocalPath string) (contract.RepoLifecycleResult, error)
	BeginLoadDump(serverID, repoID string, applyIgnorePolicy bool, keepLastRevisions *int) (contract.RepoLifecycleResult, error)
	BeginDetach(context.Context, string, string, bool) (contract.RepoLifecycleResult, error)
	Status(operationID string) (contract.RepoLifecycleResult, error)
}

type SystemLifecycleService interface {
	Restart()
	Shutdown()
}

// MobilePairingService mints a mobile pairing token for the given
// activated server profile, through the daemon's own control-plane
// channel (mirrors RepositoryLifecycleService's role for repo-create).
type MobilePairingService interface {
	Begin(ctx context.Context, serverID string) (contract.MobilePairingBeginResult, error)
}

type ServerDetachService interface {
	Detach(context.Context, string) error
}

type RealmRemovalService interface {
	Begin(context.Context, string, string, contract.RealmRemoveBeginPayload) (contract.RealmRemoveBeginResult, error)
	Confirm(context.Context, contract.RealmRemoveConfirmPayload) (contract.RealmRemoveConfirmResult, error)
	List(context.Context) ([]contract.RecoveryStatus, error)
	Download(context.Context, contract.RecoveryDownloadPayload) (contract.RecoveryDownloadResult, error)
}

type UpdateService interface {
	Status(context.Context) (contract.UpdateStatus, error)
	Plan(context.Context) (contract.UpdatePlanResult, error)
	Apply(context.Context) (contract.UpdateApplyResult, error)
}

type ActivitySource interface {
	List() []activity.Entry
}

func (s *Server) SetActivitySource(source ActivitySource) {
	s.mu.Lock()
	s.activity = source
	s.mu.Unlock()
}

func (s *Server) activitySource() ActivitySource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activity
}

func (s *Server) SetUpdateService(service UpdateService) {
	s.mu.Lock()
	s.updates = service
	s.mu.Unlock()
}

func (s *Server) updateService() UpdateService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updates
}

func (s *Server) capabilities() []string {
	caps := append([]string(nil), contract.AllCapabilities...)
	if s.systemLifecycleService() != nil {
		caps = append(caps, contract.CapSystemRestart, contract.CapSystemShutdown)
	}
	if s.updateService() != nil {
		caps = append(caps, contract.CapUpdateStatus, contract.CapUpdatePlan, contract.CapUpdateApply)
	}
	if s.activitySource() != nil {
		caps = append(caps, contract.CapRepoActivity)
	}
	return caps
}

func (s *Server) SetSystemLifecycleService(service SystemLifecycleService) {
	s.mu.Lock()
	s.lifecycleFn = service
	s.mu.Unlock()
}

func (s *Server) systemLifecycleService() SystemLifecycleService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lifecycleFn
}

func (s *Server) SetRepositoryLifecycleService(service RepositoryLifecycleService) {
	s.mu.Lock()
	s.lifecycle = service
	s.mu.Unlock()
}

func (s *Server) repositoryLifecycleService() RepositoryLifecycleService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lifecycle
}

func (s *Server) SetMobilePairingService(service MobilePairingService) {
	s.mu.Lock()
	s.mobilePair = service
	s.mu.Unlock()
}

func (s *Server) mobilePairingService() MobilePairingService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mobilePair
}

func (s *Server) SetServerDetachService(service ServerDetachService) {
	s.mu.Lock()
	s.serverDetach = service
	s.mu.Unlock()
}

func (s *Server) serverDetachService() ServerDetachService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serverDetach
}

func (s *Server) SetRealmRemovalService(service RealmRemovalService) {
	s.mu.Lock()
	s.realmRemoval = service
	s.mu.Unlock()
}

func (s *Server) realmRemovalService() RealmRemovalService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realmRemoval
}

func (s *Server) SetActivationService(service ActivationService) {
	s.mu.Lock()
	s.activation = service
	s.mu.Unlock()
}

func (s *Server) SetRealmAliasService(service RealmAliasService) {
	s.mu.Lock()
	s.realmAlias = service
	s.mu.Unlock()
}

func (s *Server) realmAliasService() RealmAliasService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realmAlias
}

func (s *Server) SetOwnerLabelResolver(resolver OwnerLabelResolver) {
	s.mu.Lock()
	s.ownerLabels = resolver
	s.mu.Unlock()
}

func (s *Server) ownerLabelResolver() OwnerLabelResolver {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerLabels
}

func (s *Server) activationService() ActivationService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activation
}

// New creates a Server that will listen on sockPath.
func New(sockPath string) *Server {
	return &Server{
		sockPath:    sockPath,
		startTime:   time.Now(),
		lg:          talk.With("ipc"),
		repos:       make(map[string]*RepoState),
		activations: make(map[string]contract.ActivationStatus),
		subs:        make(map[chan contract.Event]struct{}),
		conns:       make(map[net.Conn]struct{}),
	}
}

func (s *Server) RegisterActivation(status contract.ActivationStatus) {
	s.mu.Lock()
	s.activations[status.ServerID] = status
	s.mu.Unlock()
	s.Emit(contract.NewEvent("", 0, contract.EvActivationChanged, "", status))
}

// RemoveServer clears one profile's in-memory activation and repositories
// after its credential has been revoked and local profile removed.
func (s *Server) RemoveServer(serverID string) {
	s.mu.Lock()
	delete(s.activations, serverID)
	for id, repo := range s.repos {
		if repo.ServerID() == serverID {
			delete(s.repos, id)
		}
	}
	s.mu.Unlock()
	s.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
}

func (s *Server) SetActivationRepositoryReadiness(serverID string, ready bool, pending int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, ok := s.activations[serverID]
	if !ok {
		return
	}
	status.RepositoriesReady = ready
	status.PendingRequiredRepos = pending
	s.activations[serverID] = status
}

func (s *Server) allActivations() []contract.ActivationStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contract.ActivationStatus, 0, len(s.activations))
	for _, status := range s.activations {
		out = append(out, status)
	}
	return out
}

// DefaultSocketPath returns the canonical per-user socket path.
// Prefers $XDG_RUNTIME_DIR (Linux systemd sessions) for proper tmpfs placement.
func DefaultSocketPath() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "filees.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".filees", "daemon.sock")
}

// RegisterRepo adds a repo to the server's registry. Returns the RepoState the
// daemon should update as operations progress. Must be called before Start.
func (s *Server) RegisterRepo(id, url, localPath string) *RepoState {
	return s.RegisterRepoAccess(id, url, localPath, "default", contract.AccessReadWrite)
}

func (s *Server) RegisterRepoAccess(id, url, localPath, serverID, access string) *RepoState {
	displayName := filepath.Base(filepath.Clean(localPath))
	if displayName == "." || displayName == string(filepath.Separator) || displayName == "" {
		displayName = id
	}
	rs := &RepoState{
		server:       s,
		id:           id,
		url:          url,
		localPath:    localPath,
		serverID:     serverID,
		access:       access,
		displayName:  displayName,
		attached:     true,
		state:        contract.StateInitializing,
		connectivity: contract.ConnOnline,
	}
	s.mu.Lock()
	s.repos[id] = rs
	s.mu.Unlock()
	return rs
}

func (s *Server) RegisterProjectedRepo(id, displayName, url, serverID, access, state string, attached bool) *RepoState {
	return s.RegisterProjectedRepoPolicy(id, displayName, url, serverID, access, state, "", "optional", attached)
}

func (s *Server) RegisterProjectedRepoPolicy(id, displayName, url, serverID, access, state, ownerRealmID, attachmentPolicy string, attached bool) *RepoState {
	s.mu.Lock()
	rs := s.repos[id]
	if rs == nil {
		rs = &RepoState{server: s, id: id, serverID: serverID, connectivity: contract.ConnOnline}
		s.repos[id] = rs
	}
	s.mu.Unlock()
	rs.SetProjectedMetadata(displayName, url, access, state, ownerRealmID, attachmentPolicy, attached)
	return rs
}

// ReconcileProjectedRepos replaces the presentation knowledge for one server.
// Repositories omitted by the authoritative projection are removed from IPC;
// this never removes or otherwise mutates their local working copies.
func (s *Server) ReconcileProjectedRepos(serverID string, repos []ProjectedRepo) {
	present := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		present[repo.ID] = struct{}{}
		s.RegisterProjectedRepoPolicy(repo.ID, repo.DisplayName, repo.URL, serverID, repo.Access, repo.State, repo.OwnerRealmID, repo.AttachmentPolicy, repo.Attached)
	}
	s.mu.Lock()
	removed := false
	for id, repo := range s.repos {
		if repo.ServerID() != serverID {
			continue
		}
		if _, ok := present[id]; !ok {
			delete(s.repos, id)
			removed = true
		}
	}
	s.mu.Unlock()
	if removed {
		s.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
	}
}

// MarkRepoDetached clears only local attachment knowledge when no current
// authoritative projection is available (for example during an offline
// detach). Server-owned metadata remains presentation-only until the next
// projection refresh.
func (s *Server) MarkRepoDetached(serverID, repoID string) {
	repo := s.repoByID(repoID)
	if repo == nil || repo.ServerID() != serverID {
		return
	}
	repo.markDetached()
	s.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
}

type ProjectedRepo struct {
	ID, DisplayName, URL, Access, State string
	OwnerRealmID, AttachmentPolicy      string
	Attached                            bool
}

// NewRepoEvent builds an event envelope for the given repo.
// Sequence and EventID are intentionally zero/empty: Emit() assigns them
// inside subsMu so sequence order == delivery order.
func (s *Server) NewRepoEvent(repoID, evType string, payload any) contract.Event {
	return contract.NewEvent("", 0, evType, repoID, payload)
}

// Start binds the socket and begins accepting connections, then returns.
// Cancelling ctx closes the listener and every active request or event-stream
// connection so clients can immediately enter their reconnect flow.
func (s *Server) Start(ctx context.Context) error {
	_ = os.Remove(s.sockPath)
	if err := os.MkdirAll(filepath.Dir(s.sockPath), 0o700); err != nil {
		return err
	}

	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return err
	}
	_ = os.Chmod(s.sockPath, 0o600) // restrict to owner

	s.lg.Infof("listening on %s", s.sockPath)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
		s.closeConnections()
		_ = os.Remove(s.sockPath)
	}()

	go s.acceptLoop(ln)
	return nil
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener closed — normal on shutdown
		}
		s.connsMu.Lock()
		if s.stopping {
			s.connsMu.Unlock()
			_ = c.Close()
			continue
		}
		s.conns[c] = struct{}{}
		s.connsMu.Unlock()
		go func() {
			defer func() {
				s.connsMu.Lock()
				delete(s.conns, c)
				s.connsMu.Unlock()
			}()
			s.handleConn(c)
		}()
	}
}

func (s *Server) closeConnections() {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	s.stopping = true
	for conn := range s.conns {
		_ = conn.Close()
		delete(s.conns, conn)
	}
}

// Emit assigns a monotone sequence number and broadcasts ev to all subscribers.
// Sequence is assigned inside subsMu so delivery order == sequence order:
// two concurrent callers cannot produce seq=N delivered after seq=N+1.
// Slow subscribers receive a dropped event (non-blocking send); they must
// resync via repo.status.
func (s *Server) Emit(ev contract.Event) {
	s.subsMu.Lock()
	seq := atomic.AddInt64(&s.evSeq, 1)
	ev.Sequence = seq
	ev.EventID = fmt.Sprintf("%016x", seq)
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	s.subsMu.Unlock()
}

func (s *Server) addSub(ch chan contract.Event) {
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()
}

func (s *Server) removeSub(ch chan contract.Event) {
	s.subsMu.Lock()
	delete(s.subs, ch)
	s.subsMu.Unlock()
}

// repoByID looks up a repo under read lock. Returns nil if not found.
func (s *Server) repoByID(id string) *RepoState {
	s.mu.RLock()
	rs := s.repos[id]
	s.mu.RUnlock()
	return rs
}

// allRepos returns a snapshot slice of all registered repos.
func (s *Server) allRepos() []*RepoState {
	s.mu.RLock()
	out := make([]*RepoState, 0, len(s.repos))
	for _, rs := range s.repos {
		out = append(out, rs)
	}
	s.mu.RUnlock()
	return out
}

// uptime returns seconds since the server started.
func (s *Server) uptime() int64 {
	return int64(time.Since(s.startTime).Seconds())
}
