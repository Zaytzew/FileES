package actions

import (
	"context"
	"errors"
	"filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
	"testing"
	"time"
)

type admissionCreator struct {
	calls   int
	results []error
	id      string
}

func (a *admissionCreator) CreateAnchor(context.Context, string, string, string) (string, error) {
	a.calls++
	if a.calls <= len(a.results) {
		return a.id, a.results[a.calls-1]
	}
	return "op", nil
}
func admissionController() *Controller {
	c := &Controller{}
	c.cfg.ViewModel = func() app.ViewModel {
		return app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoAttachIntent: true, contract.CapRepoAttachApprove: true, contract.CapRepoExplorerAnchor: true}, Servers: []app.ServerViewModel{{ID: "spot", Repos: []app.RepoViewModel{{ID: "repo", State: contract.StateUnattached}}}}}
	}
	c.cfg.CreationStatusPollInterval = time.Millisecond
	c.cfg.CreationStatusPollTimeout = 50 * time.Millisecond
	return c
}
func TestAnchorAdmissionRetriesOnlyItsPreEffectRefusal(t *testing.T) {
	c := admissionController()
	busy := errcat.New("head.anchor_busy", nil, nil)
	a := &admissionCreator{results: []error{busy, busy}}
	if id, err := c.createAnchorWhenReady(t.Context(), a, "spot", "repo", "/point"); err != nil || id != "op" || a.calls != 3 {
		t.Fatal(id, err, a.calls)
	}
	for _, failure := range []error{errors.New("working copy writer is busy"), context.DeadlineExceeded, errcat.New("head.folder_busy", nil, nil), errcat.New("repo.invalid_local_intent", nil, nil)} {
		a := &admissionCreator{results: []error{failure}}
		if _, err := c.createAnchorWhenReady(t.Context(), a, "spot", "repo", "/point"); err == nil || err.Error() != failure.Error() || a.calls != 1 {
			t.Fatal(err, a.calls)
		}
	}
	a = &admissionCreator{id: "begun", results: []error{busy}}
	if _, err := c.createAnchorWhenReady(t.Context(), a, "spot", "repo", "/point"); err == nil || a.calls != 1 {
		t.Fatal("begun operation replayed")
	}
}
func TestAnchorAdmissionCancelsOrStopsWhenRepositoryNoLongerAttachable(t *testing.T) {
	c := admissionController()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a := &admissionCreator{}
	if _, err := c.createAnchorWhenReady(ctx, a, "spot", "repo", "/point"); !errors.Is(err, context.Canceled) || a.calls != 0 {
		t.Fatal(err, a.calls)
	}
	c.cfg.ViewModel = func() app.ViewModel { return app.ViewModel{} }
	if _, err := c.createAnchorWhenReady(t.Context(), a, "spot", "repo", "/point"); err == nil || a.calls != 0 {
		t.Fatal("lost permission admitted")
	}
}

func TestAnchorAdmissionTimeoutDoesNotSubmitAnotherAttempt(t *testing.T) {
	c := admissionController()
	c.cfg.CreationStatusPollTimeout = 5 * time.Millisecond
	c.cfg.CreationStatusPollInterval = 20 * time.Millisecond
	a := &admissionCreator{results: []error{errcat.New("head.anchor_busy", nil, nil)}}
	_, err := c.createAnchorWhenReady(t.Context(), a, "spot", "repo", "/point")
	var fault errcat.Fault
	if !errors.As(err, &fault) || fault.Spec.Key != "head.anchor_wait_timeout" || a.calls != 1 {
		t.Fatal(err, a.calls)
	}
}

func TestAnchorAdmissionRefusesMissingCapabilityEvenForAttachableRepository(t *testing.T) {
	c := admissionController()
	view := c.cfg.ViewModel()
	delete(view.Capabilities, contract.CapRepoExplorerAnchor)
	c.cfg.ViewModel = func() app.ViewModel { return view }
	a := &admissionCreator{}
	if _, err := c.createAnchorWhenReady(t.Context(), a, "spot", "repo", "/point"); err == nil || a.calls != 0 {
		t.Fatal("missing anchor capability admitted", err, a.calls)
	}
}
