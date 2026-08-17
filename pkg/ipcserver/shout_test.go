package ipcserver

import (
	"context"
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/shout"
)

func TestRepoPublishRequiresCommentAndPendingChanges(t *testing.T) {
	server := New(t.TempDir())
	rs := server.RegisterRepo("docs", "svn://example/docs", t.TempDir())
	rs.SetPublishFunc(func(context.Context, string) (int64, error) {
		return 0, shout.ErrNothingToPublish
	})
	resp := server.handleRepoPublish(contract.Request{
		RequestID: "p1",
		RepoID:    "docs",
		Payload:   []byte(`{"comment":"wydanie"}`),
	})
	if resp.Status != contract.StatusError || resp.Error == nil || resp.Error.MessageKey != "shout.nothing_to_publish" {
		t.Fatalf("response=%#v", resp)
	}
}

func TestNoticeListAndAck(t *testing.T) {
	server := New(t.TempDir())
	rs := server.RegisterRepo("docs", "svn://example/docs", t.TempDir())
	acked := ""
	rs.SetNoticeFuncs(func() ([]contract.Notice, error) {
		return []contract.Notice{{ID: "shout:docs:3", RepoID: "docs", Title: "paka", CreatedAt: "2026-08-17T00:00:00Z"}}, nil
	}, func(id string) error {
		acked = id
		return nil
	})
	list := server.handleNoticeList(contract.Request{RequestID: "n1", Payload: []byte(`{}`)})
	if list.Status != contract.StatusOK {
		t.Fatalf("list=%#v", list)
	}
	var result contract.NoticeListResult
	if err := contract.DecodeResult(list.Result, &result); err != nil || len(result.Notices) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	ack := server.handleNoticeAck(contract.Request{RequestID: "n2", Payload: []byte(`{"notice_id":"shout:docs:3"}`)})
	if ack.Status != contract.StatusOK || acked != "shout:docs:3" {
		t.Fatalf("ack=%#v acked=%q", ack, acked)
	}
}

func TestRepoPublishRejectsReadOnly(t *testing.T) {
	server := New(t.TempDir())
	rs := server.RegisterRepoAccess("docs", "svn://example/docs", t.TempDir(), "default", contract.AccessReadOnly)
	rs.SetPublishFunc(func(context.Context, string) (int64, error) {
		return 0, errors.New("should not run")
	})
	resp := server.handleRepoPublish(contract.Request{
		RequestID: "p2",
		RepoID:    "docs",
		Payload:   []byte(`{"comment":"x"}`),
	})
	if resp.Status != contract.StatusError || resp.Error == nil || resp.Error.MessageKey != "shout.read_only" {
		t.Fatalf("response=%#v", resp)
	}
}
