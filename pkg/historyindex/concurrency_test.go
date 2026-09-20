package historyindex

import (
	"context"
	"testing"
	"time"
)

type blockedHistorySource struct {
	entered chan struct{}
	release chan struct{}
	log     bool
}

func (s blockedHistorySource) wait(ctx context.Context) error {
	close(s.entered)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s blockedHistorySource) Head(ctx context.Context) (int64, error) {
	if !s.log {
		return 1, s.wait(ctx)
	}
	return 1, nil
}
func (s blockedHistorySource) Log(ctx context.Context, _, _ int64, _ int) ([]Commit, error) {
	if s.log {
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
	}
	return []Commit{{Revision: 1, Date: "2026-09-20T12:00:00Z"}}, nil
}

func TestCachedReadsDoNotWaitForRemoteHeadOrLog(t *testing.T) {
	for _, log := range []bool{false, true} {
		t.Run(map[bool]string{false: "head", true: "log"}[log], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			source := blockedHistorySource{entered: make(chan struct{}), release: make(chan struct{}), log: log}
			index := &Index{Dir: t.TempDir()}
			done := make(chan error, 1)
			go func() { _, _, err := index.Extend(ctx, "abcd", source, 1); done <- err }()
			<-source.entered
			read := make(chan error, 1)
			go func() { _, err := index.Aggregate("abcd", Query{BucketSeconds: 3600}); read <- err }()
			select {
			case err := <-read:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cached chart blocked on remote request")
			}
			close(source.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
