package ipcclient

import (
	"context"
	"filees/pkg/runtime"
	"github.com/google/uuid"
	"sync"
	"time"
)

// BeginShoutDraft obtains a repo-bound lease before the editor opens. A lost
// heartbeat cancels the editor; a lost process is bounded by the daemon TTL.
func (c *Client) BeginShoutDraft(ctx context.Context, repo string) (context.Context, func(), error) {
	token := uuid.NewString()
	draftCtx, cancel := context.WithCancelCause(runtime.WithDraft(ctx, token))
	var once sync.Once
	done := make(chan struct{})
	stop := func() {
		once.Do(func() {
			cancel(context.Canceled)
			<-done
			releaseCtx, end := context.WithTimeout(context.Background(), 3*time.Second)
			defer end()
			_, _ = c.ShoutDraft(releaseCtx, repo, token, "end")
		})
	}
	beginCtx, endBegin := context.WithTimeout(draftCtx, 30*time.Second)
	defer endBegin()
	action := "begin"
	for {
		ready, err := c.ShoutDraft(beginCtx, repo, token, action)
		if err != nil {
			close(done)
			stop()
			return nil, nil, err
		}
		if ready {
			break
		}
		action = "renew"
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-beginCtx.Done():
			timer.Stop()
			close(done)
			stop()
			return nil, nil, beginCtx.Err()
		case <-timer.C:
		}
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-draftCtx.Done():
				return
			case <-ticker.C:
				renewCtx, end := context.WithTimeout(draftCtx, 5*time.Second)
				_, err := c.ShoutDraft(renewCtx, repo, token, "renew")
				end()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return draftCtx, stop, nil
}
