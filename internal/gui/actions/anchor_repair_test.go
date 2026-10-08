package actions

import (
	"context"
	"reflect"
	"testing"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	contract "filees/pkg/contract/v1"
)

type scriptedAnchorRepairer struct {
	outcomes []AnchorRepairOutcome
	calls    []string
}

func (r *scriptedAnchorRepairer) RepairAnchor(_ context.Context, root string, reattachOnly bool) (AnchorRepairOutcome, error) {
	call := "repair " + root
	if reattachOnly {
		call = "reattach " + root
	}
	r.calls = append(r.calls, call)
	outcome := r.outcomes[0]
	if len(r.outcomes) > 1 {
		r.outcomes = r.outcomes[1:]
	}
	return outcome, nil
}

func TestRepairAnchorAsksFirstAndReportsWhatWindowsDid(t *testing.T) {
	const root = `F:\work\ZUGA`
	damaged := app.RepoViewModel{ID: "repo", ServerID: "s", DisplayName: "ZUGA", Attached: true, LocalPath: root, AnchorDamage: &contract.AnchorDamage{Entries: 10}}
	for _, test := range []struct {
		name     string
		repo     app.RepoViewModel
		answers  []bool // to the consent screen, then to each "switch back on"
		outcomes []AnchorRepairOutcome
		calls    []string
		confirms []string
		info     string
	}{
		{name: "declined", repo: damaged, answers: []bool{false}, confirms: []string{"confirm.repairAnchor"}},
		{name: "repaired", repo: damaged, answers: []bool{true}, outcomes: []AnchorRepairOutcome{AnchorRepairDone},
			calls: []string{"repair " + root}, confirms: []string{"confirm.repairAnchor"}, info: "info.repairAnchorDone"},
		{name: "no administrator rights", repo: damaged, answers: []bool{true}, outcomes: []AnchorRepairOutcome{AnchorRepairNoRights},
			calls: []string{"repair " + root}, confirms: []string{"confirm.repairAnchor"}, info: "info.repairAnchorNoRights"},
		{name: "some entries stayed", repo: damaged, answers: []bool{true}, outcomes: []AnchorRepairOutcome{AnchorRepairIncomplete},
			calls: []string{"repair " + root}, confirms: []string{"confirm.repairAnchor"}, info: "info.repairAnchorIncomplete"},
		{name: "nothing removed", repo: damaged, answers: []bool{true}, outcomes: []AnchorRepairOutcome{AnchorRepairFailed},
			calls: []string{"repair " + root}, confirms: []string{"confirm.repairAnchor"}, info: "info.repairAnchorUnchanged"},
		{name: "filter stayed off, switched back on at the second request", repo: damaged, answers: []bool{true, true, true},
			outcomes: []AnchorRepairOutcome{AnchorRepairFilterDetached, AnchorRepairNoRights, AnchorRepairDone},
			calls:    []string{"repair " + root, "reattach " + root, "reattach " + root},
			confirms: []string{"confirm.repairAnchor", "confirm.repairAnchorReattach", "confirm.repairAnchorReattach"}, info: "info.repairAnchorDone"},
		{name: "filter stayed off and the person closed the window", repo: damaged, answers: []bool{true, false},
			outcomes: []AnchorRepairOutcome{AnchorRepairFilterDetached},
			calls:    []string{"repair " + root}, confirms: []string{"confirm.repairAnchor", "confirm.repairAnchorReattach"}},
		{name: "nothing damaged", repo: app.RepoViewModel{ID: "repo", ServerID: "s", Attached: true, LocalPath: root}},
		{name: "not attached", repo: app.RepoViewModel{ID: "repo", ServerID: "s", LocalPath: root, AnchorDamage: &contract.AnchorDamage{Entries: 3}}},
		{name: "relative path", repo: app.RepoViewModel{ID: "repo", ServerID: "s", Attached: true, LocalPath: `work\ZUGA`, AnchorDamage: &contract.AnchorDamage{Entries: 3}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			vm := app.ViewModel{Connected: true, Repos: []app.RepoViewModel{test.repo}}
			repairer := &scriptedAnchorRepairer{outcomes: test.outcomes}
			var confirms []string
			var info string
			answers := test.answers
			fake := &platformtest.Fake{
				ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
					confirms = append(confirms, request.PresentationKey)
					if request.PresentationArgs["count"] != "10" || request.PresentationArgs["volume"] != "F:" || request.PresentationArgs["path"] != root || request.PresentationArgs["name"] != "ZUGA" {
						t.Errorf("arguments: %v", request.PresentationArgs)
					}
					answer := answers[0]
					answers = answers[1:]
					return answer, nil
				},
				ShowInfoFunc: func(_ context.Context, request platform.InfoRequest) error {
					info = request.PresentationKey
					return nil
				},
			}
			c := New(Config{ViewModel: func() app.ViewModel { return vm }, Prompter: fake, Notifier: fake, AnchorRepairer: repairer})
			c.startRepairAnchor(context.Background(), "s", "repo")
			c.tasks.Wait()
			if !reflect.DeepEqual(repairer.calls, test.calls) {
				t.Errorf("elevated calls %q, want %q", repairer.calls, test.calls)
			}
			if !reflect.DeepEqual(confirms, test.confirms) {
				t.Errorf("questions %q, want %q", confirms, test.confirms)
			}
			if info != test.info {
				t.Errorf("result window %q, want %q", info, test.info)
			}
		})
	}
}

func TestRepairAnchorDoesNotElevateOnStaleState(t *testing.T) {
	const root = `F:\work\ZUGA`
	vm := app.ViewModel{Connected: true, Repos: []app.RepoViewModel{{ID: "repo", ServerID: "s", Attached: true, LocalPath: root, AnchorDamage: &contract.AnchorDamage{Entries: 2}}}}
	repairer := &scriptedAnchorRepairer{outcomes: []AnchorRepairOutcome{AnchorRepairDone}}
	fake := &platformtest.Fake{ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) {
		// Repaired by someone else while the question was on screen.
		vm = app.ViewModel{Connected: true, Repos: []app.RepoViewModel{{ID: "repo", ServerID: "s", Attached: true, LocalPath: root}}}
		return true, nil
	}}
	c := New(Config{ViewModel: func() app.ViewModel { return vm }, Prompter: fake, Notifier: fake, AnchorRepairer: repairer})
	c.startRepairAnchor(context.Background(), "s", "repo")
	c.tasks.Wait()
	if len(repairer.calls) != 0 {
		t.Fatalf("elevated on stale state: %q", repairer.calls)
	}
}
