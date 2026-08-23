package main

import (
	"context"
	"sync"

	"filees/internal/gui/platform"
)

const promptSnapshotEvent = "filees:prompt-snapshot"

type PromptService struct {
	gate     sync.Mutex
	mu       sync.RWMutex
	snapshot PromptSnapshot
	emitter  snapshotEmitter
	show     func()
	hide     func()
	active   *promptSession
	revision uint64
}

type promptSession struct {
	result   chan PromptChoice
	resolved bool
}

type PromptSnapshot struct {
	Revision    uint64 `json:"revision"`
	Mode        string `json:"mode"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	Default     string `json:"default,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	ConfirmText string `json:"confirm_text"`
	CancelText  string `json:"cancel_text,omitempty"`
}

type PromptChoice struct {
	Revision  uint64 `json:"revision"`
	Confirmed bool   `json:"confirmed"`
	Value     string `json:"value,omitempty"`
}

type PromptAcceptance struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code,omitempty"`
}

func newPromptService() *PromptService { return &PromptService{} }

func (service *PromptService) attachEmitter(emitter snapshotEmitter) {
	service.mu.Lock()
	service.emitter = emitter
	service.mu.Unlock()
}

func (service *PromptService) attachPresentation(show, hide func()) {
	service.mu.Lock()
	service.show, service.hide = show, hide
	service.mu.Unlock()
}

func (service *PromptService) Snapshot() PromptSnapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.snapshot
}

func (service *PromptService) Resolve(choice PromptChoice) PromptAcceptance {
	service.mu.Lock()
	session := service.active
	if session == nil || choice.Revision != service.snapshot.Revision {
		service.mu.Unlock()
		return PromptAcceptance{Code: "prompt_context_changed"}
	}
	if session.resolved {
		service.mu.Unlock()
		return PromptAcceptance{Code: "prompt_busy"}
	}
	session.resolved = true
	service.mu.Unlock()
	session.result <- choice
	return PromptAcceptance{Accepted: true}
}

func (service *PromptService) Cancel() {
	service.mu.RLock()
	revision := service.snapshot.Revision
	service.mu.RUnlock()
	service.Resolve(PromptChoice{Revision: revision})
}

func (service *PromptService) PromptText(ctx context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
	choice, err := service.present(ctx, PromptSnapshot{Mode: "text", Title: request.Title, Text: request.Text, Default: request.Default, Placeholder: request.Placeholder, Secret: request.Secret, ConfirmText: "Zatwierdź", CancelText: "Anuluj"})
	return platform.PromptTextResult{Value: choice.Value, Cancelled: !choice.Confirmed}, err
}

func (service *PromptService) Confirm(ctx context.Context, request platform.ConfirmRequest) (bool, error) {
	choice, err := service.present(ctx, PromptSnapshot{Mode: "confirm", Title: request.Title, Text: request.Text, ConfirmText: request.ConfirmText, CancelText: request.CancelText})
	return choice.Confirmed, err
}

func (service *PromptService) ShowInfo(ctx context.Context, request platform.InfoRequest) error {
	_, err := service.present(ctx, PromptSnapshot{Mode: "info", Title: request.Title, Text: request.Text, ConfirmText: "Rozumiem"})
	return err
}

func (service *PromptService) present(ctx context.Context, snapshot PromptSnapshot) (PromptChoice, error) {
	service.gate.Lock()
	defer service.gate.Unlock()
	if snapshot.ConfirmText == "" {
		snapshot.ConfirmText = "Dalej"
	}
	if snapshot.Mode != "info" && snapshot.CancelText == "" {
		snapshot.CancelText = "Anuluj"
	}
	session := &promptSession{result: make(chan PromptChoice, 1)}
	service.mu.Lock()
	service.revision++
	snapshot.Revision = service.revision
	service.snapshot, service.active = snapshot, session
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	if emitter != nil {
		emitter.Emit(promptSnapshotEvent, snapshot)
	}
	if show != nil {
		show()
	}
	var choice PromptChoice
	var err error
	select {
	case choice = <-session.result:
	case <-ctx.Done():
		err = ctx.Err()
	}
	service.mu.Lock()
	if service.active == session {
		service.active = nil
	}
	hide := service.hide
	service.mu.Unlock()
	if hide != nil {
		hide()
	}
	return choice, err
}

var _ platform.Prompter = (*PromptService)(nil)
