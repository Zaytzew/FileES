// Package reposupervisor reconciles server projections with live repository
// pipelines. It owns lifecycle ordering, not SVN or watcher implementation.
package reposupervisor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Key struct{ ServerID, RepoID string }

func (k Key) String() string { return k.ServerID + "/" + k.RepoID }

type Desired struct {
	Key         Key
	Access      string
	State       string
	URL         string
	DisplayName string
	// EditingPolicy decides whether the pipeline runs the edit passport
	// machinery. Like Access and URL it comes from the server projection and
	// must take part in the restart comparison below: the passport manager is
	// built once at start, so a policy that changes while URL and Access stay
	// put would otherwise never take effect.
	EditingPolicy string
	// SessionTimeout is how long one send or fetch may run. A change must
	// start the folder's work again, or the new wait would not apply until
	// something else restarted it.
	SessionTimeout time.Duration
}

type Instance interface{ Stop(context.Context) error }
type Starter interface {
	Start(context.Context, Desired) (Instance, error)
}

type Event struct {
	Key    Key
	Action string
	Access string
}

type live struct {
	desired  Desired
	instance Instance
}

type Supervisor struct {
	mu         sync.Mutex
	starter    Starter
	generation map[string]int64
	live       map[Key]live
	onEvent    func(Event)
}

func New(starter Starter, onEvent func(Event)) (*Supervisor, error) {
	if starter == nil {
		return nil, errors.New("repository starter is required")
	}
	return &Supervisor{starter: starter, generation: make(map[string]int64), live: make(map[Key]live), onEvent: onEvent}, nil
}

// Apply reconciles one server projection. Stop always completes before a
// replacement start, so rw->r cannot overlap watcher/committer with update-only.
func (s *Supervisor) Apply(ctx context.Context, serverID string, generation int64, desired []Desired) error {
	return s.ApplyWithTransition(ctx, serverID, generation, desired, nil)
}

// ApplyWithTransition invokes transition after the old instance has stopped
// and before its replacement starts. The daemon uses this boundary to publish
// changed access only when the old writer can no longer commit.
func (s *Supervisor) ApplyWithTransition(ctx context.Context, serverID string, generation int64, desired []Desired, transition func(Desired)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(ctx, serverID, generation, desired, transition, false)
}

// ApplyLocalAttachment reapplies the current authoritative generation after a
// local path is attached. It never accepts an older or speculative server
// generation; only local topology is allowed to change at this boundary.
func (s *Supervisor) ApplyLocalAttachment(ctx context.Context, serverID string, generation int64, desired []Desired, transition func(Desired)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(ctx, serverID, generation, desired, transition, true)
}

// DetachLocal stops exactly one local pipeline without requiring a current
// server projection. Detach changes local topology only, so it must remain
// available while the realm is offline or its cached projection is missing.
func (s *Supervisor) DetachLocal(ctx context.Context, key Key) error {
	if strings.TrimSpace(key.ServerID) == "" || strings.TrimSpace(key.RepoID) == "" {
		return errors.New("repository key is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, running := s.live[key]
	if !running {
		return nil
	}
	if err := item.instance.Stop(ctx); err != nil {
		return fmt.Errorf("stop repository %s: %w", key, err)
	}
	delete(s.live, key)
	s.emit(Event{Key: key, Action: "stopped", Access: item.desired.Access})
	return nil
}

// SuspendServer stops every local data pipeline for one server without
// advancing or discarding its authoritative projection generation. It is the
// runtime counterpart of a revoked client proof: no working copy may keep
// polling with credentials the server has terminally refused. A later explicit
// activation can reapply the same cached generation through
// ApplyLocalAttachment; a normal projection refresh still has to be newer.
func (s *Supervisor) SuspendServer(ctx context.Context, serverID string) error {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return errors.New("server ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.serverKeys(serverID, nil)
	for _, key := range keys {
		item, running := s.live[key]
		if !running {
			continue
		}
		if err := item.instance.Stop(ctx); err != nil {
			return fmt.Errorf("suspend repository %s: %w", key, err)
		}
		delete(s.live, key)
		s.emit(Event{Key: key, Action: "stopped", Access: item.desired.Access})
	}
	return nil
}

func (s *Supervisor) applyLocked(ctx context.Context, serverID string, generation int64, desired []Desired, transition func(Desired), localReapply bool) error {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" || generation < 1 {
		return errors.New("server ID and positive generation are required")
	}
	if localReapply && generation != s.generation[serverID] {
		return fmt.Errorf("local attachment generation %d does not match authoritative generation %d", generation, s.generation[serverID])
	}
	if !localReapply && generation <= s.generation[serverID] {
		return fmt.Errorf("projection generation %d is not newer than %d", generation, s.generation[serverID])
	}
	next := make(map[Key]Desired, len(desired))
	for _, item := range desired {
		if item.Key.ServerID != serverID || item.Key.RepoID == "" {
			return errors.New("projection repository key is invalid")
		}
		if item.Access != "r" && item.Access != "rw" {
			return fmt.Errorf("repository %s access is invalid", item.Key)
		}
		if item.State != "initializing" && item.State != "active" && item.State != "disabled" && item.State != "revoked" {
			return fmt.Errorf("repository %s state is invalid", item.Key)
		}
		if _, exists := next[item.Key]; exists {
			return fmt.Errorf("repository %s is duplicated", item.Key)
		}
		next[item.Key] = item
	}
	keys := s.serverKeys(serverID, next)
	for _, key := range keys {
		old, running := s.live[key]
		wanted, present := next[key]
		shouldRun := present && wanted.State == "active"
		// Every field compared here is one the running instance baked in at
		// start time and cannot pick up live, so each must force a restart.
		unchanged := running && shouldRun && old.desired.Access == wanted.Access && old.desired.URL == wanted.URL && old.desired.EditingPolicy == wanted.EditingPolicy && old.desired.SessionTimeout == wanted.SessionTimeout
		if unchanged {
			old.desired = wanted
			s.live[key] = old
			continue
		}
		if running {
			if err := old.instance.Stop(ctx); err != nil {
				return fmt.Errorf("stop repository %s: %w", key, err)
			}
			delete(s.live, key)
			s.emit(Event{Key: key, Action: "stopped", Access: old.desired.Access})
		}
		if present && transition != nil {
			transition(wanted)
		}
		if shouldRun {
			instance, err := s.starter.Start(ctx, wanted)
			if err != nil {
				return fmt.Errorf("start repository %s: %w", key, err)
			}
			s.live[key] = live{desired: wanted, instance: instance}
			s.emit(Event{Key: key, Action: "started", Access: wanted.Access})
		}
	}
	if !localReapply {
		s.generation[serverID] = generation
	}
	return nil
}

func (s *Supervisor) serverKeys(serverID string, next map[Key]Desired) []Key {
	set := make(map[Key]struct{}, len(next))
	for key := range next {
		set[key] = struct{}{}
	}
	for key := range s.live {
		if key.ServerID == serverID {
			set[key] = struct{}{}
		}
	}
	keys := make([]Key, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

func (s *Supervisor) emit(event Event) {
	if s.onEvent != nil {
		s.onEvent(event)
	}
}

func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]Key, 0, len(s.live))
	for key := range s.live {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	var first error
	for _, key := range keys {
		item := s.live[key]
		if err := item.instance.Stop(ctx); err != nil && first == nil {
			first = err
		}
		delete(s.live, key)
		s.emit(Event{Key: key, Action: "stopped", Access: item.desired.Access})
	}
	return first
}
