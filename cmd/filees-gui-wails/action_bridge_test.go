package main

import (
	"context"
	"errors"
	"testing"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
)

type pendingDeleteClient struct{}

type shoutClientStub struct {
	repoID, comment, noticeID string
}

type realmGrantClientStub struct {
	editing contract.RepoSetEditingPolicyPayload
}

type uploadChannelClientStub struct {
	created contract.UploadChannelCreatePayload
	updated contract.UploadChannelUpdatePayload
}

func (stub *uploadChannelClientStub) UploadChannelList(context.Context, string, string) (*contract.UploadChannelListResult, error) {
	return &contract.UploadChannelListResult{Channels: []contract.UploadChannelSummary{{ChannelID: "channel-1", Alias: "acme", Slug: "inbox", State: "active", Recipients: []string{"a@example.net"}}}}, nil
}
func (stub *uploadChannelClientStub) UploadChannelCreate(_ context.Context, payload contract.UploadChannelCreatePayload) (*contract.UploadChannelResult, error) {
	stub.created = payload
	return &contract.UploadChannelResult{ChannelID: "channel-1", State: "active"}, nil
}
func (stub *uploadChannelClientStub) UploadChannelUpdate(_ context.Context, payload contract.UploadChannelUpdatePayload) (*contract.UploadChannelResult, error) {
	stub.updated = payload
	return &contract.UploadChannelResult{ChannelID: payload.ChannelID, State: "active"}, nil
}
func (stub *uploadChannelClientStub) UploadChannelRevoke(_ context.Context, payload contract.UploadChannelChannelPayload) (*contract.UploadChannelResult, error) {
	return &contract.UploadChannelResult{ChannelID: payload.ChannelID, State: "revoked"}, nil
}
func (stub *uploadChannelClientStub) UploadChannelDelete(_ context.Context, payload contract.UploadChannelChannelPayload) (*contract.UploadChannelResult, error) {
	return &contract.UploadChannelResult{ChannelID: payload.ChannelID, State: "deleted"}, nil
}

type dumpLoadClientStub struct {
	serverID, repoID string
	applyIgnore      bool
}

type serverDetachClientStub struct{ serverID string }

func (stub *serverDetachClientStub) ServerDetach(_ context.Context, serverID string) (*contract.ServerDetachResult, error) {
	stub.serverID = serverID
	return &contract.ServerDetachResult{ServerID: serverID}, nil
}

type realmRemovalClientStub struct {
	begin   contract.RealmRemoveBeginPayload
	confirm contract.RealmRemoveConfirmPayload
}

func (stub *realmRemovalClientStub) RealmRemoveBegin(_ context.Context, payload contract.RealmRemoveBeginPayload) (*contract.RealmRemoveBeginResult, error) {
	stub.begin = payload
	return &contract.RealmRemoveBeginResult{OperationID: "operation-1", RecoveryKitPath: "/tmp/recovery.fkr", ActiveClientCount: 2, OwnedRepositoryCount: 3, ForeignGrantCount: 4}, nil
}
func (stub *realmRemovalClientStub) RealmRemoveConfirm(_ context.Context, payload contract.RealmRemoveConfirmPayload) (*contract.RealmRemoveConfirmResult, error) {
	stub.confirm = payload
	return &contract.RealmRemoveConfirmResult{RecoveryKitPath: payload.RecoveryKitPath, ArchiveCount: 3, ErasureRequested: true, ErasureMaxDays: 30}, nil
}

type consentPrompterStub struct {
	answers []bool
	calls   []platform.ConfirmRequest
}

type updateClientStub struct{ applied bool }

type realmAliasClientStub struct{ serverID, alias string }

func (stub *realmAliasClientStub) RealmAliasClaim(_ context.Context, serverID, alias string) (*contract.RealmAliasClaimResult, error) {
	stub.serverID, stub.alias = serverID, alias
	return &contract.RealmAliasClaimResult{Alias: alias}, nil
}

type mobilePairingClientStub struct{ serverID string }

func (stub *mobilePairingClientStub) MobilePairingBegin(_ context.Context, serverID string) (*contract.MobilePairingBeginResult, error) {
	stub.serverID = serverID
	return &contract.MobilePairingBeginResult{Address: "spot:2223", HostPublicKey: "ssh-ed25519 AAAA", Token: "secret", ExpiresAt: "tomorrow"}, nil
}

func (*updateClientStub) UpdatePlan(context.Context) (*contract.UpdatePlanResult, error) {
	return &contract.UpdatePlanResult{CurrentVersion: "602", AvailableVersion: "603", ReleaseID: "r603", RestartRequired: true, Changes: []contract.UpdateChange{{Action: "update", Path: "/usr/local/bin/filees", Detail: "sha256"}}}, nil
}
func (stub *updateClientStub) UpdateApply(context.Context) (*contract.UpdateApplyResult, error) {
	stub.applied = true
	return &contract.UpdateApplyResult{InstalledVersion: "603", RestartRequired: true}, nil
}

func (stub *consentPrompterStub) Confirm(_ context.Context, request platform.ConfirmRequest) (bool, error) {
	stub.calls = append(stub.calls, request)
	answer := stub.answers[0]
	stub.answers = stub.answers[1:]
	return answer, nil
}
func (*consentPrompterStub) PromptText(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
	return platform.PromptTextResult{Cancelled: true}, nil
}
func (*consentPrompterStub) ShowInfo(context.Context, platform.InfoRequest) error { return nil }

func (stub *dumpLoadClientStub) RepoLoadDump(_ context.Context, serverID, repoID string, applyIgnore bool, keep *int) (*contract.RepoLifecycleResult, error) {
	stub.serverID, stub.repoID, stub.applyIgnore = serverID, repoID, applyIgnore
	if keep != nil {
		return nil, errors.New("unexpected bounded history")
	}
	return &contract.RepoLifecycleResult{State: "loading"}, nil
}

func (stub *realmGrantClientStub) RealmGrantRecipients(context.Context, string, string) (*contract.RealmGrantRecipientsResult, error) {
	return &contract.RealmGrantRecipientsResult{}, nil
}
func (stub *realmGrantClientStub) RealmSetVisibility(_ context.Context, _ string, visibility string) (*contract.RealmSetVisibilityResult, error) {
	return &contract.RealmSetVisibilityResult{Visibility: visibility}, nil
}
func (stub *realmGrantClientStub) RepoGrantAccess(_ context.Context, payload contract.RepoGrantAccessPayload) (*contract.RealmGrantResult, error) {
	return &contract.RealmGrantResult{RepoID: payload.RepoID, RecipientRealmID: payload.RecipientRealmID, Access: payload.Access, State: "active"}, nil
}
func (stub *realmGrantClientStub) RepoRevokeAccess(_ context.Context, payload contract.RepoRevokeAccessPayload) (*contract.RealmGrantResult, error) {
	return &contract.RealmGrantResult{RepoID: payload.RepoID, RecipientRealmID: payload.RecipientRealmID, State: "revoked"}, nil
}
func (stub *realmGrantClientStub) RepoSetEditingPolicy(_ context.Context, payload contract.RepoSetEditingPolicyPayload) (*contract.RepoSetEditingPolicyResult, error) {
	stub.editing = payload
	return &contract.RepoSetEditingPolicyResult{RepoID: payload.RepoID, Policy: payload.Policy}, nil
}

func (stub *shoutClientStub) RepoPublish(_ context.Context, repoID, comment string) (*contract.RepoPublishResult, error) {
	stub.repoID, stub.comment = repoID, comment
	return &contract.RepoPublishResult{Revision: 17}, nil
}

func (stub *shoutClientStub) NoticeAck(_ context.Context, noticeID string) error {
	stub.noticeID = noticeID
	return nil
}

func (pendingDeleteClient) RepoDetach(context.Context, string, string) (*contract.RepoLifecycleResult, error) {
	return &contract.RepoLifecycleResult{State: "detached"}, nil
}

func (pendingDeleteClient) RepoDelete(context.Context, string, string) (*contract.RepoLifecycleResult, error) {
	return &contract.RepoLifecycleResult{State: "deleting", ServerDeleteCompleted: true, LocalCleanupCompleted: true, LastError: "recovery pending"}, nil
}

func TestRepositoryDetachAdapterAcceptsDurableServerDeletion(t *testing.T) {
	adapter := repositoryDetachAdapter{client: pendingDeleteClient{}}
	if err := adapter.DetachRepository(t.Context(), "office", "repo-1", true); err != nil {
		t.Fatalf("durable server deletion rejected while recovery is pending: %v", err)
	}
}

func TestShoutAdapterPublishesAndAcknowledgesThroughIPC(t *testing.T) {
	client := &shoutClientStub{}
	adapter := shoutAdapter{client: client}
	revision, err := adapter.Publish(t.Context(), "docs", "gotowe do odbioru")
	if err != nil || revision != 17 || client.repoID != "docs" || client.comment != "gotowe do odbioru" {
		t.Fatalf("Publish() revision=%d client=%+v err=%v", revision, client, err)
	}
	if err := adapter.AckNotice(t.Context(), "notice-1"); err != nil || client.noticeID != "notice-1" {
		t.Fatalf("AckNotice() client=%+v err=%v", client, err)
	}
}

func TestRealmGrantAdapterTranslatesEditingPolicy(t *testing.T) {
	client := &realmGrantClientStub{}
	adapter := realmGrantAdapter{client: client}
	stored, err := adapter.SetEditingPolicy(t.Context(), "spot", "docs", true)
	if err != nil || !stored || client.editing.ServerID != "spot" || client.editing.RepoID != "docs" || client.editing.Policy != contract.EditingLockRequired {
		t.Fatalf("SetEditingPolicy(true) stored=%v payload=%+v err=%v", stored, client.editing, err)
	}
	stored, err = adapter.SetEditingPolicy(t.Context(), "spot", "docs", false)
	if err != nil || stored || client.editing.Policy != contract.EditingFree {
		t.Fatalf("SetEditingPolicy(false) stored=%v payload=%+v err=%v", stored, client.editing, err)
	}
}

func TestUploadChannelAdapterTranslatesDeclarations(t *testing.T) {
	client := &uploadChannelClientStub{}
	adapter := uploadChannelAdapter{client: client}
	channels, err := adapter.ListUploadChannels(t.Context(), "spot", "docs")
	if err != nil || len(channels) != 1 || channels[0].Slug != "inbox" || len(channels[0].Recipients) != 1 {
		t.Fatalf("ListUploadChannels() = %+v, %v", channels, err)
	}
	declaration := actions.UploadChannelDeclaration{AuthorityRepoID: "docs", Slug: "drop", Recipients: []string{"a@example.net"}}
	if err := adapter.CreateUploadChannel(t.Context(), "spot", declaration); err != nil || client.created.AuthorityRepoID != "docs" || client.created.Slug != "drop" {
		t.Fatalf("CreateUploadChannel() payload=%+v err=%v", client.created, err)
	}
	if err := adapter.UpdateUploadChannel(t.Context(), "spot", "channel-1", declaration); err != nil || client.updated.ChannelID != "channel-1" {
		t.Fatalf("UpdateUploadChannel() payload=%+v err=%v", client.updated, err)
	}
}

func TestRepositoryDumpLoaderUsesFullHistoryAndIgnorePolicy(t *testing.T) {
	client := &dumpLoadClientStub{}
	if err := (repositoryDumpLoadAdapter{client: client}).LoadDump(t.Context(), "spot", "docs"); err != nil {
		t.Fatalf("LoadDump() err=%v", err)
	}
	if client.serverID != "spot" || client.repoID != "docs" || !client.applyIgnore {
		t.Fatalf("LoadDump() client=%+v", client)
	}
}

func TestServerAndRealmLifecycleAdaptersTranslateIPC(t *testing.T) {
	detachClient := &serverDetachClientStub{}
	if err := (serverDetachAdapter{client: detachClient}).DetachServer(t.Context(), "spot"); err != nil || detachClient.serverID != "spot" {
		t.Fatalf("DetachServer() server=%q err=%v", detachClient.serverID, err)
	}
	removeClient := &realmRemovalClientStub{}
	adapter := realmRemovalAdapter{client: removeClient}
	begin, err := adapter.BeginRealmRemoval(t.Context(), actions.RealmRemovalBeginRequest{ServerID: "spot", NotificationEmail: "a@example.net", RecoveryDirectory: "/tmp", ErasureRequested: true})
	if err != nil || begin.OperationID != "operation-1" || removeClient.begin.ServerID != "spot" || !removeClient.begin.ErasureRequested {
		t.Fatalf("BeginRealmRemoval() result=%+v payload=%+v err=%v", begin, removeClient.begin, err)
	}
	confirmed, err := adapter.ConfirmRealmRemoval(t.Context(), "spot", begin.OperationID, []byte("123456"), begin.RecoveryKitPath)
	if err != nil || confirmed.ArchiveCount != 3 || removeClient.confirm.OperationID != "operation-1" || string(removeClient.confirm.OTP) != "123456" {
		t.Fatalf("ConfirmRealmRemoval() result=%+v payload=%+v err=%v", confirmed, removeClient.confirm, err)
	}
}

func TestConsentPromptAdapterKeepsRequiredAndOptionalDecisionsSeparate(t *testing.T) {
	prompter := &consentPrompterStub{answers: []bool{true, false}}
	result, err := (consentPromptAdapter{prompter: prompter}).ConfirmConsent(t.Context(), platform.ConsentRequest{Title: "Retencja", Text: "Polityka", RequiredText: "Rozumiem", OptionalText: "Usuń wszystko"})
	if err != nil || result.Cancelled || !result.Required || result.Optional || len(prompter.calls) != 2 {
		t.Fatalf("ConfirmConsent() result=%+v calls=%+v err=%v", result, prompter.calls, err)
	}
	cancelled := &consentPrompterStub{answers: []bool{false}}
	result, err = (consentPromptAdapter{prompter: cancelled}).ConfirmConsent(t.Context(), platform.ConsentRequest{})
	if err != nil || !result.Cancelled || len(cancelled.calls) != 1 {
		t.Fatalf("required refusal result=%+v calls=%d err=%v", result, len(cancelled.calls), err)
	}
}

func TestUpdateAdapterProjectsPlanAndApplyResult(t *testing.T) {
	client := &updateClientStub{}
	adapter := updateAdapter{client: client}
	plan, err := adapter.UpdatePlan(t.Context())
	if err != nil || plan.AvailableVersion != "603" || plan.ReleaseID != "r603" || len(plan.Changes) != 1 || plan.Changes[0].Path != "/usr/local/bin/filees" {
		t.Fatalf("UpdatePlan() = %+v, %v", plan, err)
	}
	result, err := adapter.UpdateApply(t.Context())
	if err != nil || !client.applied || result.InstalledVersion != "603" || !result.RestartRequired {
		t.Fatalf("UpdateApply() = %+v applied=%v err=%v", result, client.applied, err)
	}
}

func TestAliasAndMobilePairingAdaptersUseSelectedServer(t *testing.T) {
	aliasClient := &realmAliasClientStub{}
	if err := (realmAliasAdapter{client: aliasClient}).ClaimAlias(t.Context(), "spot", "acme"); err != nil || aliasClient.serverID != "spot" || aliasClient.alias != "acme" {
		t.Fatalf("ClaimAlias() client=%+v err=%v", aliasClient, err)
	}
	pairClient := &mobilePairingClientStub{}
	want := errors.New("helper unavailable")
	err := (mobilePairingAdapter{client: pairClient, helperPath: func() (string, error) { return "", want }}).Launch(t.Context(), "spot")
	if !errors.Is(err, want) || pairClient.serverID != "spot" {
		t.Fatalf("Launch() server=%q err=%v", pairClient.serverID, err)
	}
}

func TestValidateSystemLifecycleResult(t *testing.T) {
	if err := validateSystemLifecycleResult(&contract.SystemLifecycleResult{Action: "restart"}, "restart", nil); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if err := validateSystemLifecycleResult(nil, "restart", nil); err == nil {
		t.Fatal("empty result accepted")
	}
	if err := validateSystemLifecycleResult(&contract.SystemLifecycleResult{Action: "shutdown"}, "restart", nil); err == nil {
		t.Fatal("unexpected action accepted")
	}
	want := errors.New("ipc failed")
	if got := validateSystemLifecycleResult(nil, "restart", want); !errors.Is(got, want) {
		t.Fatalf("transport error = %v, want %v", got, want)
	}
}
