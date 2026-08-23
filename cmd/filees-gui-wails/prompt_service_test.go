package main

import (
	"context"
	"testing"
	"time"

	"filees/internal/gui/platform"
)

func TestPromptServiceReturnsBrowserChoice(t *testing.T) {
	service := newPromptService()
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	result := make(chan platform.PromptTextResult, 1)
	go func() {
		got, _ := service.PromptText(context.Background(), platform.PromptTextRequest{Title: "Limit", Default: "30"})
		result <- got
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("prompt window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "text" || snapshot.Default != "30" || snapshot.Revision == 0 {
		t.Fatalf("unexpected projection: %+v", snapshot)
	}
	if accepted := service.Resolve(PromptChoice{Revision: snapshot.Revision, Confirmed: true, Value: "90"}); !accepted.Accepted {
		t.Fatalf("choice rejected: %+v", accepted)
	}
	select {
	case got := <-result:
		if got.Cancelled || got.Value != "90" {
			t.Fatalf("unexpected result: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt did not resolve")
	}
}
