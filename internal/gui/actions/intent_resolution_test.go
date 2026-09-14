package actions_test

import (
	"context"
	"errors"
	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"strings"
	"testing"
	"time"
)

type fakeIntentResolver struct{ calls chan string }

func (f *fakeIntentResolver) PlanCommitRecovery(context.Context, string) (*contract.CommitRecoveryPlan, error) {
	return nil, errors.New("unused")
}

type fakeCommitRecoveryResolver struct{ calls chan string }

func (f *fakeCommitRecoveryResolver) PlanCommitRecovery(_ context.Context, repoID string) (*contract.CommitRecoveryPlan, error) {
	return &contract.CommitRecoveryPlan{PlanID: "recovery-plan", RepoID: repoID, TransactionID: "transaction-1", Choice: contract.CommitRecoveryRetryQueue, FirstRevision: 44, HeadRevision: 43, Paths: []string{"old/folder", "new/file.pdf"}}, nil
}
func (f *fakeCommitRecoveryResolver) ApplyCommitRecovery(_ context.Context, repoID, planID, choice string) error {
	f.calls <- repoID + ":" + planID + ":" + choice
	return nil
}
func (f *fakeCommitRecoveryResolver) PlanIntents(context.Context, string) (*actions.IntentResolutionPlan, error) {
	return nil, errors.New("unused")
}
func (f *fakeCommitRecoveryResolver) ApplyIntents(context.Context, string, string, string) error {
	return errors.New("unused")
}

func TestCommitRecoveryDialogUsesServerProofAndAppliesExactPlan(t *testing.T) {
	resolver := &fakeCommitRecoveryResolver{calls: make(chan string, 1)}
	shown := make(chan platform.ConfirmRequest, 1)
	fake := &platformtest.Fake{SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
		return platform.SettingsDialogResult{Action: platform.SettingsDialogResolveCommitRecovery, ServerID: "office", RepoID: "repo-1"}, nil
	}, ConfirmFunc: func(_ context.Context, r platform.ConfirmRequest) (bool, error) { shown <- r; return true, nil }}
	view := lifecycleView(contract.CapRepoCommitRecovery)
	view.Repos[0].CommitRecoveryRequired = true
	view.Servers[0].Repos[0] = view.Repos[0]
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: fake, Prompter: fake, IntentResolver: resolver})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	dialog := awaitCh(t, shown, "commit recovery confirmation")
	if dialog.PresentationKey != "details.commitRecovery" || dialog.PresentationArgs["firstRevision"] != "44" || dialog.PresentationArgs["headRevision"] != "43" || dialog.PresentationArgs["pathCount"] != "2" {
		t.Fatalf("dialog=%+v", dialog)
	}
	if got := awaitCh(t, resolver.calls, "commit recovery decision"); got != "repo-1:recovery-plan:"+contract.CommitRecoveryRetryQueue {
		t.Fatal(got)
	}
}
func (f *fakeIntentResolver) ApplyCommitRecovery(context.Context, string, string, string) error {
	return errors.New("unused")
}

func (f *fakeIntentResolver) PlanIntents(_ context.Context, repoID string) (*actions.IntentResolutionPlan, error) {
	return &actions.IntentResolutionPlan{ID: "opaque-plan", RepoID: repoID, Choice: "delete_add", Paths: []actions.IntentResolutionPath{{Path: "new V2.txt", Operation: "add", Size: 12}, {Path: "old.txt", Operation: "delete"}}}, nil
}
func (f *fakeIntentResolver) ApplyIntents(_ context.Context, repoID, id, choice string) error {
	f.calls <- repoID + ":" + id + ":" + choice
	return nil
}

func TestIntentDialogConfirmationAndCancellation(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "confirm"}[confirm], func(t *testing.T) {
			resolver := &fakeIntentResolver{calls: make(chan string, 1)}
			shown := make(chan platform.ConfirmRequest, 1)
			fake := &platformtest.Fake{SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
				return platform.SettingsDialogResult{Action: platform.SettingsDialogResolveIntents, ServerID: "office", RepoID: "repo-1"}, nil
			}, ConfirmFunc: func(_ context.Context, r platform.ConfirmRequest) (bool, error) { shown <- r; return confirm, nil }}
			view := lifecycleView(contract.CapRepoIntentResolution)
			view.Repos[0].Pending.RenameUncertain = 1
			view.Servers[0].Repos[0] = view.Repos[0]
			lifecycle := newRecordingActionLifecycle()
			intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: fake, Prompter: fake, IntentResolver: resolver, ActionLifecycle: lifecycle})
			defer cancel()
			send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
			dialog := awaitCh(t, shown, "intent confirmation")
			if !strings.Contains(dialog.Text, "DODAJ: new V2.txt") || !strings.Contains(dialog.Text, "USUŃ: old.txt") {
				t.Fatalf("dialog=%+v", dialog)
			}
			if confirm {
				if got := awaitCh(t, resolver.calls, "decision"); got != "repo-1:opaque-plan:delete_add" {
					t.Fatal(got)
				}
				started := awaitCh(t, lifecycle.started, "projection action")
				if !started.ExpectedIntentsResolved {
					t.Fatal("missing projection fence")
				}
				if got := awaitCh(t, lifecycle.awaited, "projection wait"); got != started.ID {
					t.Fatal(got)
				}
			} else {
				select {
				case <-resolver.calls:
					t.Fatal("cancel submitted decision")
				case <-time.After(100 * time.Millisecond):
				}
			}
		})
	}
}
