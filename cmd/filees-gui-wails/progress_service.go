package main

import (
	"context"
	"maps"
	"sort"
	"sync"
	"time"

	"filees/internal/gui/platform"
)

const progressEvent = "filees:progress"

// ProgressService is the Wails platform.ProgressPresenter: the long waits the
// action controller explains (creating a repository, its first publication,
// the first checkout of an attached one) become an overlay over the whole main
// window (frontend/progress-overlay.js).
//
// Until 2026-09-24 the Wails interface wired no presenter at all, so these
// waits were silent: after "Create" the confirmation stayed on screen for
// tens of seconds and the first publication showed only as a queue label -
// the owner's Fedora acceptance read it as something having broken.
type ProgressService struct {
	mu      sync.Mutex
	emitter snapshotEmitter
	next    uint64
	active  map[uint64]ProgressItem
}

// ProgressItem is one explained wait, as the overlay renders it.
type ProgressItem struct {
	ID               uint64            `json:"id"`
	PresentationKey  string            `json:"presentation_key,omitempty"`
	PresentationArgs map[string]string `json:"presentation_args,omitempty"`
	Title            string            `json:"title"`
	Text             string            `json:"text"`
	StartedAt        string            `json:"started_at"`
}

// ProgressSnapshot lists the waits in the order they began.
type ProgressSnapshot struct {
	Items []ProgressItem `json:"items"`
}

func newProgressService() *ProgressService {
	return &ProgressService{active: map[uint64]ProgressItem{}}
}

func (service *ProgressService) attachEmitter(emitter snapshotEmitter) {
	service.mu.Lock()
	service.emitter = emitter
	service.mu.Unlock()
}

// ShowProgress opens one wait and answers the function that closes it. The
// closer is idempotent: the controller may call it from a defer and earlier.
func (service *ProgressService) ShowProgress(_ context.Context, request platform.ProgressRequest) (func(), error) {
	service.mu.Lock()
	service.next++
	id := service.next
	service.active[id] = ProgressItem{
		ID: id, PresentationKey: request.PresentationKey, PresentationArgs: maps.Clone(request.PresentationArgs),
		Title: request.Title, Text: request.Text, StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	service.mu.Unlock()
	service.emit()
	var once sync.Once
	return func() {
		once.Do(func() {
			service.mu.Lock()
			delete(service.active, id)
			service.mu.Unlock()
			service.emit()
		})
	}, nil
}

// Snapshot answers the waits in progress, oldest first.
func (service *ProgressService) Snapshot() ProgressSnapshot {
	service.mu.Lock()
	defer service.mu.Unlock()
	items := make([]ProgressItem, 0, len(service.active))
	for _, item := range service.active {
		item.PresentationArgs = maps.Clone(item.PresentationArgs)
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return ProgressSnapshot{Items: items}
}

func (service *ProgressService) emit() {
	service.mu.Lock()
	emitter := service.emitter
	service.mu.Unlock()
	if emitter != nil {
		emitter.Emit(progressEvent, service.Snapshot())
	}
}

var _ platform.ProgressPresenter = (*ProgressService)(nil)
