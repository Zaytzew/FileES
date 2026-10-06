package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	guiapp "filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
)

type consentProbe struct {
	calls []contract.RepoAnchorConsentPayload
	err   error
	wrong bool
}

func (d *consentProbe) AnchorConsent(_ context.Context, p contract.RepoAnchorConsentPayload) (*contract.RepoAnchorConsentResult, error) {
	d.calls = append(d.calls, p)
	return &contract.RepoAnchorConsentResult{Allowed: p.Allow != d.wrong}, d.err
}

func TestAnchorConsentDialogRechecksContextAndDefaultsToDeny(t *testing.T) {
	for _, variant := range []string{"allow", "deny", "cancel", "error", "wrong-result", "stale", "offline", "detach", "move", "capability", "block", "unknown-choice", "display", "initial-stale"} {
		t.Run(variant, func(t *testing.T) {
			block := contract.AnchorDownloadBlock{ID: strings.Repeat("ab", 16), Program: "reader.exe", Files: 21}
			vm := guiapp.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapRepoAnchorConsent: true}, Servers: []guiapp.ServerViewModel{{ID: "office", Repos: []guiapp.RepoViewModel{{ID: "repo", Attached: true, Access: "r", Sparse: true, LocalPath: "point", DisplayName: "Drawings", AnchorDownloadBlocks: []contract.AnchorDownloadBlock{block}}}}}}
			service := &GUIService{view: vm}
			candidate := anchorConsentCandidates(vm)[0]
			d := &consentProbe{wrong: variant == "wrong-result"}
			if variant == "error" {
				d.err = errors.New("expired")
			}
			if variant == "display" {
				service.recoveringDisplay = true
			}
			if variant == "initial-stale" {
				service.view.Stale = true
			}
			prompts := 0
			shown, err := service.answerAnchorConsent(t.Context(), candidate, d, func(_ context.Context, p PromptSelectRequest) (PromptSelectResult, error) {
				prompts++
				if p.Default != "deny" || !p.Radio || len(p.Options) != 2 || p.Options[0].Value != "deny" || !strings.Contains(p.Text, "21") || !strings.Contains(p.Text, "reader.exe") {
					t.Fatal(p)
				}
				switch variant {
				case "stale":
					service.view.Stale = true
				case "offline":
					service.view.Connected = false
				case "detach":
					service.view.Servers[0].Repos[0].Attached = false
				case "move":
					service.view.Servers[0].Repos[0].LocalPath = "different"
				case "capability":
					delete(service.view.Capabilities, contract.CapRepoAnchorConsent)
				case "block":
					service.view.Servers[0].Repos[0].AnchorDownloadBlocks[0].ID = strings.Repeat("cd", 16)
				}
				value := "allow"
				if variant == "deny" {
					value = "deny"
				}
				if variant == "unknown-choice" {
					value = "any-program"
				}
				return PromptSelectResult{Value: value, Cancelled: variant == "cancel"}, nil
			})
			switch variant {
			case "allow", "deny", "error", "wrong-result":
				if !shown || prompts != 1 || len(d.calls) != 1 || d.calls[0].BlockID != candidate.block.ID || d.calls[0].Allow != (variant != "deny") {
					t.Fatal(shown, prompts, d.calls, err)
				}
				if (err != nil) != (variant == "error" || variant == "wrong-result") {
					t.Fatal(err)
				}
			case "display", "initial-stale":
				if shown || prompts != 0 || len(d.calls) != 0 {
					t.Fatal("stale display prompted")
				}
			default:
				if !shown || prompts != 1 || len(d.calls) != 0 || err != nil {
					t.Fatal("unconfirmed mutation", shown, prompts, d.calls, err)
				}
			}
		})
	}
}
