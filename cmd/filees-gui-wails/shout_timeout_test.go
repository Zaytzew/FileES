package main

import (
	"context"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

type deadlineShoutClient struct {
	deadline    time.Time
	hasDeadline bool
}

func (c *deadlineShoutClient) BeginShoutDraft(ctx context.Context, _ string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}
func (c *deadlineShoutClient) SyncPause(context.Context, bool) error   { return nil }
func (c *deadlineShoutClient) NoticeAck(context.Context, string) error { return nil }
func (c *deadlineShoutClient) RepoPublish(ctx context.Context, _, _ string) (*contract.RepoPublishResult, error) {
	c.deadline, c.hasDeadline = ctx.Deadline()
	return &contract.RepoPublishResult{Revision: 7}, nil
}

// repo.publish sends everything queued and the daemon allows itself 30
// minutes. The GUI must wait at least that long, not the IPC client's
// 10-second default that ended a release in "daemon.sock: i/o timeout".
func TestShoutPublishWaitsAsLongAsTheDaemonAllows(t *testing.T) {
	client := &deadlineShoutClient{}
	started := time.Now()
	rev, err := (shoutAdapter{client: client}).Publish(context.Background(), "repo", "komentarz")
	if err != nil || rev != 7 {
		t.Fatalf("rev=%d err=%v", rev, err)
	}
	if !client.hasDeadline || client.deadline.Sub(started) < 30*time.Minute {
		t.Fatalf("deadline %v after start, want at least the daemon's 30 minutes", client.deadline.Sub(started))
	}
}
