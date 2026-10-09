package main

import (
	"context"
	"errors"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"strings"
	"testing"
)

type headMoveProbe struct {
	fakeHeadBrowserDaemon
	plans, writes, checks int
	fail                  bool
}

func (f *headMoveProbe) AnchorMovePlan(_ context.Context, p contract.RepoAnchorMoveSelection) (*contract.RepoAnchorMovePlan, error) {
	f.plans++
	return &contract.RepoAnchorMovePlan{Source: p.Source, Destination: p.Destination, Fingerprint: strings.Repeat("ab", 32)}, nil
}
func (f *headMoveProbe) AnchorMove(_ context.Context, p contract.RepoAnchorMovePayload) (*contract.RepoAnchorMoveResult, error) {
	f.writes++
	if f.fail {
		return nil, errors.New("fixture failed")
	}
	return &contract.RepoAnchorMoveResult{State: "moved"}, nil
}
func (f *headMoveProbe) AnchorMoveRecover(context.Context, contract.RepoAnchorMoveSelection) (*contract.RepoAnchorMoveResult, error) {
	f.checks++
	return &contract.RepoAnchorMoveResult{State: "moved"}, nil
}
func TestHeadBrowserMoveConsentFocusAndSpinner(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "focus", "revoke", "error"} {
		t.Run(mode, func(t *testing.T) {
			snapshot := headBrowserSnapshot()
			snapshot.Capabilities = append(snapshot.Capabilities, contract.CapRepoAnchorMove)
			snapshot.Repositories[1].Access = "rw"
			probe := &headMoveProbe{}
			service := newHeadBrowserService(probe, func() Snapshot { return snapshot }, func(code, key string, _ map[string]string) string { return key }, func(_ string, fallback string) string { return fallback })
			service.Open("office", "sparse")
			service.confirm = func(_ context.Context, p platform.ConfirmRequest) (bool, error) {
				if p.PresentationArgs["source"] != "old" || p.PresentationArgs["destination"] != "parent/new" {
					t.Fatal(p)
				}
				if mode == "focus" {
					service.Open("office", "remote")
				}
				if mode == "revoke" {
					snapshot.Repositories[1].Access = "r"
				}
				return mode != "cancel", nil
			}
			opened, closed := 0, 0
			service.showProgress = func(context.Context, platform.ProgressRequest) (func(), error) {
				opened++
				return func() { closed++ }, nil
			}
			probe.fail = mode == "error"
			out, err := service.MoveFolder("old", "parent/new")
			wantWrites := 0
			if mode == "success" || mode == "error" {
				wantWrites = 1
			}
			if probe.plans != 1 || probe.writes != wantWrites || opened != wantWrites || closed != wantWrites {
				t.Fatal(probe, opened, closed, err)
			}
			if mode == "success" && (err != nil || out.State != "moved") {
				t.Fatal(out, err)
			}
			if mode == "cancel" && (err != nil || out.State != "cancelled") {
				t.Fatal(out, err)
			}
			if (mode == "focus" || mode == "revoke" || mode == "error") && err == nil {
				t.Fatal("unsafe or failed operation reported success")
			}
		})
	}
}
