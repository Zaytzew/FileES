// Package ipcserver implements the FileES daemon-side IPC contract server.
// It listens on a Unix domain socket, speaks filees.contract/v1 over JSON Lines,
// and is the single point of contact between the daemon engine and all clients.
package ipcserver

import (
	"context"
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

	mu    sync.RWMutex
	repos map[string]*RepoState // keyed by repo ID

	evSeq int64 // atomic monotone counter for Event.Sequence

	subsMu sync.Mutex
	subs   map[chan contract.Event]struct{}
}

// New creates a Server that will listen on sockPath.
func New(sockPath string) *Server {
	return &Server{
		sockPath:  sockPath,
		startTime: time.Now(),
		lg:        talk.With("ipc"),
		repos:     make(map[string]*RepoState),
		subs:      make(map[chan contract.Event]struct{}),
	}
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
	rs := &RepoState{
		id:           id,
		url:          url,
		localPath:    localPath,
		state:        contract.StateInitializing,
		connectivity: contract.ConnOnline,
	}
	s.mu.Lock()
	s.repos[id] = rs
	s.mu.Unlock()
	return rs
}

// Start binds the socket and begins accepting connections. Blocks until ctx is
// cancelled, then closes the listener and returns. Existing connections continue
// until the client disconnects.
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
		go s.handleConn(c)
	}
}

// Emit broadcasts ev to all current event subscribers. Slow subscribers receive
// a dropped event (non-blocking send); they must resync via repo.status.
func (s *Server) Emit(ev contract.Event) {
	s.subsMu.Lock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	s.subsMu.Unlock()
}

// NextSeq returns the next monotone event sequence number.
func (s *Server) NextSeq() int64 {
	return atomic.AddInt64(&s.evSeq, 1)
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
