//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"testing"

	"filees/pkg/localrepo"
)

func TestAnchorDetachWaitsForConnectionBeforeTouchingFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	record := localrepo.Record{ServerID: "server", RepoID: "repo", Anchor: true}
	stopped := false
	m := &anchorManager{running: map[string]anchorConnection{
		anchorKey(record): {cancel: func() { stopped = true; cancel() }, done: make(chan struct{})},
	}}
	if err := m.detach(ctx, record); !errors.Is(err, context.Canceled) || !stopped {
		t.Fatalf("must stop and wait before invoking helper: stopped=%v err=%v", stopped, err)
	}
}
