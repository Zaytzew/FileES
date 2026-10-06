package actions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type faultCreator struct{ *fakeRepositoryCreator }

func (f faultCreator) CreationFault(context.Context, string) (string, string, string, *platform.ProgressMeasure, error) {
	return "repository_created", "repo.initial_content_unavailable", "fixture diagnostic", nil, nil
}

func TestCreationRefusalUsesDurableDomainKeyAndEndsPolling(t *testing.T) {
	creator := faultCreator{&fakeRepositoryCreator{calls: make(chan createCall, 1)}}
	fake := repositoryCreationPlatform()
	view := app.ViewModel{Connected: true, Servers: []app.ServerViewModel{{ID: "office", ClientRole: contract.ClientRoleNormal, CanCreateRepositories: true}}}
	intents, cancel := setup(actions.Config{
		ViewModel: func() app.ViewModel { return view }, FolderPicker: fake, Prompter: fake, RepositoryCreator: creator, Notifier: fake,
		DomainText: func(code, key string, details map[string]string) string {
			if key != "repo.initial_content_unavailable" {
				t.Errorf("wrong domain key: %s", key)
			}
			return "Pobierz dane u dostawcy i ponów próbę"
		},
		CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: time.Second,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentCreateRepository, ServerID: "office"})
	awaitCh(t, creator.calls, "creation")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, n := range fake.Snapshot().Notifications {
			if strings.Contains(n.Body, "Pobierz dane u dostawcy i ponów próbę") {
				if !strings.Contains(n.Body, "fixture diagnostic") || !strings.Contains(n.Body, "Ponowienie użyje już utworzonego repozytorium") {
					t.Fatal(n.Body)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("missing actionable refusal")
}
