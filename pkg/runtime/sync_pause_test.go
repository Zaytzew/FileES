package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPauseReasonsAreIndependentAndDraftWaitsForAdmittedCommit(t *testing.T) {
	p := &SyncPause{}
	leave, err := p.Enter(t.Context(), "a", true)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := p.Draft("editor", "a", "begin")
	if err != nil || ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	if _, err = p.Enter(t.Context(), "b", true); !errors.Is(err, ErrSyncPaused) {
		t.Fatal(err)
	}
	leave()
	leave()
	ready, err = p.Draft("editor", "a", "renew")
	if err != nil || !ready {
		t.Fatal(ready, err)
	}
	p.SetManual(true)
	if _, err = p.Enter(t.Context(), "b", false); !errors.Is(err, ErrSyncPaused) {
		t.Fatal(err)
	}
	_, _ = p.Draft("editor", "a", "end")
	manual, draft, _ := p.Status()
	if !manual || draft {
		t.Fatal(manual, draft)
	}
	_, _ = p.Draft("editor2", "b", "begin")
	p.SetManual(false)
	if _, err = p.Enter(t.Context(), "a", true); !errors.Is(err, ErrSyncPaused) {
		t.Fatal(err)
	}
	poll, err := p.Enter(t.Context(), "a", false)
	if err != nil {
		t.Fatal(err)
	}
	poll()
}
func TestExpiredEditorCannotPublishOrReleaseAnotherEditor(t *testing.T) {
	now := time.Now()
	p := &SyncPause{now: func() time.Time { return now }}
	_, _ = p.Draft("one", "a", "begin")
	now = now.Add(DraftTTL)
	if _, err := p.Draft("one", "a", "renew"); !errors.Is(err, ErrDraftExpired) {
		t.Fatal(err)
	}
	leave, err := p.Enter(t.Context(), "b", true)
	if err != nil {
		t.Fatal(err)
	}
	leave()
	_, _ = p.Draft("two", "b", "begin")
	_, _ = p.Draft("one", "a", "end")
	if _, err := p.Enter(WithDraft(t.Context(), "one"), "a", true); !errors.Is(err, ErrDraftExpired) {
		t.Fatal(err)
	}
	if _, err := p.Enter(WithDraft(t.Context(), "two"), "a", true); !errors.Is(err, ErrDraftExpired) {
		t.Fatal(err)
	}
	leave, err = p.Enter(WithDraft(t.Context(), "two"), "b", true)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * DraftTTL)
	if _, err := p.Enter(context.Background(), "a", true); !errors.Is(err, ErrSyncPaused) {
		t.Fatal("TTL unblocked an in-flight publication", err)
	}
	leave()
	leave, err = p.Enter(t.Context(), "a", true)
	if err != nil {
		t.Fatal(err)
	}
	leave()
}
