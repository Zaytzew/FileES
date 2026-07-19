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

	contract "filees/pkg/contract/v1"
	"filees/pkg/talk"
)

// Server is the IPC contract server. Create with New, register repos with
// RegisterRepo, then call Start. Safe for concurrent use.
type Server struct {
	sockPath  string
	startTime time.Time
	lg        talk.Logger

	mu          sync.RWMutex
	repos       map[string]*RepoState // keyed by repo ID
	activations map[string]contract.ActivationStatus
	activation  ActivationService

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

func (s *Server) SetActivationService(service ActivationService) {
	s.mu.Lock()
	s.activation = service
	s.mu.Unlock()
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
	rs := &RepoState{
		server:       s,
		id:           id,
		url:          url,
		localPath:    localPath,
		serverID:     serverID,
		access:       access,
		displayName:  id,
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
