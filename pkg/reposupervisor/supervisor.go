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
)

type Key struct{ ServerID, RepoID string }

func (k Key) String() string { return k.ServerID + "/" + k.RepoID }

type Desired struct {
	Key         Key
	Access      string
	State       string
	URL         string
	DisplayName string
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
		unchanged := running && shouldRun && old.desired.Access == wanted.Access && old.desired.URL == wanted.URL
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
