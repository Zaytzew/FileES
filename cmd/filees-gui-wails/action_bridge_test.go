package main

import (
	"context"
	"errors"
	"testing"

	"filees/internal/gui/actions"
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
