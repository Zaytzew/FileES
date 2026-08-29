package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/png"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localpin"
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
	return &contract.MobilePairingBeginResult{Address: "spot:2223", HostPublicKey: "ssh-ed25519 AAAA", Token: "secret", ExpiresAt: "2099-01-02T03:04:05Z"}, nil
}

type mobilePairingPrompterStub struct {
	results        []platform.PromptTextResult
	requests       []platform.PromptTextRequest
	selectResults  []PromptSelectResult
	selectRequests []PromptSelectRequest
}

func (stub *mobilePairingPrompterStub) PromptText(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
	stub.requests = append(stub.requests, request)
	if len(stub.results) == 0 {
		return platform.PromptTextResult{Cancelled: true}, nil
	}
	result := stub.results[0]
	stub.results = stub.results[1:]
	return result, nil
}
func (*mobilePairingPrompterStub) Confirm(context.Context, platform.ConfirmRequest) (bool, error) {
	return false, nil
}
func (*mobilePairingPrompterStub) ShowInfo(context.Context, platform.InfoRequest) error { return nil }
func (stub *mobilePairingPrompterStub) SelectOne(_ context.Context, request PromptSelectRequest) (PromptSelectResult, error) {
	stub.selectRequests = append(stub.selectRequests, request)
	if len(stub.selectResults) == 0 {
		return PromptSelectResult{Cancelled: true}, nil
	}
	result := stub.selectResults[0]
	stub.selectResults = stub.selectResults[1:]
	return result, nil
}

type mobilePairingPresenterStub struct {
	presentation pairingPresentation
	calls        int
}

func (stub *mobilePairingPresenterStub) Present(_ context.Context, presentation pairingPresentation) error {
	stub.presentation = presentation
	stub.calls++
	return nil
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
	pinStore, err := localpin.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pinStore.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	pairClient := &mobilePairingClientStub{}
	prompter := &mobilePairingPrompterStub{results: []platform.PromptTextResult{{Value: "4242"}}, selectResults: []PromptSelectResult{{Value: "archive"}}}
	presenter := &mobilePairingPresenterStub{}
	err = (mobilePairingAdapter{client: pairClient, pinStore: pinStore, prompter: prompter, presenter: presenter, servers: func() []PromptOption {
		return []PromptOption{{Value: "spot", Label: "Spot", Detail: "spot:2223"}, {Value: "archive", Label: "Archiwum", Detail: "archive:2223"}}
	}}).Launch(t.Context(), "spot")
	if err != nil || pairClient.serverID != "archive" || presenter.calls != 1 {
		t.Fatalf("Launch() server=%q presenter=%+v err=%v", pairClient.serverID, presenter, err)
	}
	if len(prompter.selectRequests) != 1 || prompter.selectRequests[0].Default != "spot" || len(prompter.selectRequests[0].Options) != 2 {
		t.Fatalf("server selection prompt=%+v", prompter.selectRequests)
	}
	if !strings.HasPrefix(presenter.presentation.QRDataURL, "data:image/png;base64,") || presenter.presentation.Address != "spot:2223" {
		t.Fatalf("unexpected native pairing presentation: %+v", presenter.presentation)
	}
	encoded := strings.TrimPrefix(presenter.presentation.QRDataURL, "data:image/png;base64,")
	raw, decodeErr := base64.StdEncoding.DecodeString(encoded)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	config, decodeErr := png.DecodeConfig(bytes.NewReader(raw))
	if decodeErr != nil || config.Width != mobilePairingQRSize || config.Height != mobilePairingQRSize {
		t.Fatalf("pairing QR dimensions=%dx%d err=%v", config.Width, config.Height, decodeErr)
	}
	if presenter.presentation.ExpiresAt != time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC) {
		t.Fatalf("pairing expiry=%s", presenter.presentation.ExpiresAt)
	}
}

func TestMobilePairingDoesNotMintTokenBeforePINAuthorization(t *testing.T) {
	pinStore, err := localpin.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pinStore.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	client := &mobilePairingClientStub{}
	prompter := &mobilePairingPrompterStub{results: []platform.PromptTextResult{{Cancelled: true}}}
	err = (mobilePairingAdapter{client: client, pinStore: pinStore, prompter: prompter, presenter: &mobilePairingPresenterStub{}}).Launch(t.Context(), "spot")
	if err != nil || client.serverID != "" {
		t.Fatalf("cancelled Launch() minted token for %q or returned err=%v", client.serverID, err)
	}
}

func TestMobilePairingDoesNotMintTokenWhenServerSelectionIsCancelled(t *testing.T) {
	pinStore, err := localpin.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pinStore.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	client := &mobilePairingClientStub{}
	prompter := &mobilePairingPrompterStub{
		results:       []platform.PromptTextResult{{Value: "4242"}},
		selectResults: []PromptSelectResult{{Cancelled: true}},
	}
	err = (mobilePairingAdapter{client: client, pinStore: pinStore, prompter: prompter, presenter: &mobilePairingPresenterStub{}, servers: func() []PromptOption {
		return []PromptOption{{Value: "spot", Label: "Spot"}, {Value: "archive", Label: "Archiwum"}}
	}}).Launch(t.Context(), "spot")
	if err != nil || client.serverID != "" || len(prompter.selectRequests) != 1 {
		t.Fatalf("cancelled server choice minted token for %q, prompts=%d err=%v", client.serverID, len(prompter.selectRequests), err)
	}
}

func TestMobilePairingRetriesWrongPINAndPreservesAndroidPayload(t *testing.T) {
	pinStore, err := localpin.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pinStore.Setup([]byte("4242")); err != nil {
		t.Fatal(err)
	}
	client := &mobilePairingClientStub{}
	prompter := &mobilePairingPrompterStub{results: []platform.PromptTextResult{{Value: "0000"}, {Value: "4242"}}, selectResults: []PromptSelectResult{{Value: "spot"}}}
	presenter := &mobilePairingPresenterStub{}
	if err := (mobilePairingAdapter{client: client, pinStore: pinStore, prompter: prompter, presenter: presenter}).Launch(t.Context(), "spot"); err != nil {
		t.Fatal(err)
	}
	if len(prompter.requests) != 2 || prompter.requests[0].Label != "PIN" || prompter.requests[1].Label != "PIN" || !strings.Contains(prompter.requests[1].Text, "Nieprawidłowy") {
		t.Fatalf("PIN prompts=%+v", prompter.requests)
	}
	payload, err := mobilePairingPayloadJSON(&contract.MobilePairingBeginResult{Address: "spot:2223", HostPublicKey: "ssh-ed25519 AAAA", Token: "secret", ExpiresAt: "ignored"})
	if err != nil || string(payload) != `{"address":"spot:2223","host_public_key":"ssh-ed25519 AAAA","token":"secret"}` {
		t.Fatalf("mobile QR payload=%s err=%v", payload, err)
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
