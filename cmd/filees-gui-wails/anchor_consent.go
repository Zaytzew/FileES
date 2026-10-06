package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
)

type anchorConsentDaemon interface {
	AnchorConsent(context.Context, contract.RepoAnchorConsentPayload) (*contract.RepoAnchorConsentResult, error)
}

type anchorConsentCandidate struct {
	server, repo, path, name string
	block                    contract.AnchorDownloadBlock
}

func anchorConsentCandidates(vm guiapp.ViewModel) []anchorConsentCandidate {
	if !vm.Connected || vm.Stale || !vm.Capabilities[contract.CapRepoAnchorConsent] {
		return nil
	}
	var candidates []anchorConsentCandidate
	for _, server := range vm.Servers {
		for _, repo := range server.Repos {
			if !repo.Attached || !repo.Sparse || repo.LocalPath == "" || (repo.Access != "r" && repo.Access != "rw") {
				continue
			}
			for _, block := range repo.AnchorDownloadBlocks {
				id, err := hex.DecodeString(block.ID)
				if err != nil || len(id) != 16 || block.Program == "" || strings.ContainsAny(block.Program, "\x00\r\n\t") || block.Files < 1 {
					continue
				}
				candidates = append(candidates, anchorConsentCandidate{server: server.ID, repo: repo.ID, path: repo.LocalPath, name: repo.DisplayName, block: block})
			}
		}
	}
	return candidates
}

func (c anchorConsentCandidate) key() string { return c.server + "\x00" + c.repo + "\x00" + c.block.ID }

// Recheck both identity and attachment after the dialog. Stale GUI state is
// never permission. The daemon checks the live session again on receipt.
func (service *GUIService) answerAnchorConsent(ctx context.Context, candidate anchorConsentCandidate,
	client anchorConsentDaemon, prompt func(context.Context, PromptSelectRequest) (PromptSelectResult, error)) (bool, error) {
	current := func() bool {
		for _, fresh := range anchorConsentCandidates(service.viewModel()) {
			if fresh == candidate {
				return true
			}
		}
		return false
	}
	if !current() || client == nil || prompt == nil {
		return false, nil
	}
	finish, err := service.beginDisplayCall()
	if err != nil {
		return false, nil // retry after display recovery, never replay consent
	}
	defer finish()
	text := service.localizeText
	choice, err := prompt(ctx, PromptSelectRequest{
		Title:   text("anchorConsent.title", "Wstrzymano automatyczne pobieranie"),
		Text:    fmt.Sprintf(text("anchorConsent.body", "Program %s zażądał w krótkim czasie %d różnych plików z folderu %s. To liczba zaobserwowanych żądań, nie rozmiar całego pobrania. Zgoda pozwoli temu programowi pobierać także kolejne pliki, bez limitu strażnika, do końca sesji punktu zaczepienia. Może to pobrać całe repozytorium. Odrzucone otwarcie trzeba ponowić."), candidate.block.Program, candidate.block.Files, candidate.name),
		Default: "deny", Radio: true,
		Options: []PromptOption{
			{Value: "deny", Label: text("anchorConsent.deny", "Pozostaw blokadę")},
			{Value: "allow", Label: text("anchorConsent.allow", "Zezwól temu programowi do końca sesji")},
		},
		ConfirmText: text("anchorConsent.confirm", "Zastosuj"), CancelText: text("anchorConsent.cancel", "Nie teraz"),
	})
	if err != nil || choice.Cancelled {
		return true, err // no request, cancellation cannot grant permission
	}
	if choice.Value != "deny" && choice.Value != "allow" || !current() {
		return true, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := client.AnchorConsent(callCtx, contract.RepoAnchorConsentPayload{ServerID: candidate.server, RepoID: candidate.repo, BlockID: candidate.block.ID, Allow: choice.Value == "allow"})
	if err != nil {
		return true, err
	}
	if result == nil || result.Allowed != (choice.Value == "allow") {
		return true, fmt.Errorf("%s", text("anchorConsent.notConfirmed", "Nie udało się potwierdzić decyzji. FileES nie ponowi jej automatycznie."))
	}
	return true, nil
}

// One serial prompt worker; snapshots retain pending refusals when GUI was
// closed. The seen set only keeps IDs in the current fresh projection, so it
// cannot grow with old sessions. No polling of files or extra event stream.
func (service *GUIService) runAnchorConsents(ctx context.Context, client anchorConsentDaemon, prompts *PromptService, notifier platform.Notifier) {
	seen := make(map[string]bool)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		vm := service.viewModel()
		if vm.Connected && !vm.Stale {
			candidates := anchorConsentCandidates(vm)
			present := make(map[string]bool, len(candidates))
			for _, c := range candidates {
				present[c.key()] = true
			}
			for key := range seen {
				if !present[key] {
					delete(seen, key)
				}
			}
			for _, c := range candidates {
				if seen[c.key()] {
					continue
				}
				_, notified := seen[c.key()]
				if !notified && notifier != nil {
					_ = notifier.Notify(ctx, platform.Notification{ID: "anchor." + c.block.ID, Group: "anchor.downloads", Title: service.localizeText("anchorConsent.title", "Wstrzymano automatyczne pobieranie"), Body: c.block.Program + " — " + c.name, Urgency: platform.UrgencyNormal})
				}
				seen[c.key()] = false
				shown, err := service.answerAnchorConsent(ctx, c, client, prompts.SelectOne)
				if shown {
					seen[c.key()] = true
				}
				if err != nil && ctx.Err() == nil {
					if finish, admissionErr := service.beginDisplayCall(); admissionErr == nil {
						_ = prompts.ShowInfo(ctx, platform.InfoRequest{Title: service.localizeText("anchorConsent.title", "Wstrzymano automatyczne pobieranie"), Text: service.localizeText("anchorConsent.notConfirmed", "Nie udało się potwierdzić decyzji. FileES nie ponowi jej automatycznie.")})
						finish()
					}
				}
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
