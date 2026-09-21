package main

import (
	"context"
	"errors"
	"testing"

	"filees/internal/gui/platform"
)

type initialChannelPromptStub struct {
	choice  PromptSelectResult
	request PromptSelectRequest
	info    platform.InfoRequest
}

func (p *initialChannelPromptStub) SelectOne(_ context.Context, request PromptSelectRequest) (PromptSelectResult, error) {
	p.request = request
	return p.choice, nil
}

func (p *initialChannelPromptStub) ShowInfo(_ context.Context, request platform.InfoRequest) error {
	p.info = request
	return nil
}

func TestInitialChannelDecision(t *testing.T) {
	for _, channel := range []string{"alpha", "beta", "stable", "", "cancel"} {
		t.Run(channel, func(t *testing.T) {
			p := &initialChannelPromptStub{choice: PromptSelectResult{Value: channel, Cancelled: channel == "cancel"}}
			saved := ""
			err := chooseInitialChannel(t.Context(), p, func(value string) error { saved = value; return nil })
			valid := channel == "alpha" || channel == "beta"
			if (err == nil) != valid || (saved != "") != valid {
				t.Fatalf("saved=%q err=%v", saved, err)
			}
			if p.request.PresentationKey != "select.updateChannel" || p.request.Default != "beta" || len(p.request.Options) != 2 {
				t.Fatalf("request=%+v", p.request)
			}
		})
	}
}

func TestInitialChannelWriteErrorIsNotSuccessfulSetup(t *testing.T) {
	p := &initialChannelPromptStub{choice: PromptSelectResult{Value: "beta"}}
	failure := errors.New("configuration write failed")
	err := chooseInitialChannel(t.Context(), p, func(string) error { return failure })
	if !errors.Is(err, failure) || p.info.PresentationKey != "info.updateChannelFailed" || p.info.PresentationArgs["reason"] != failure.Error() {
		t.Fatalf("info=%+v err=%v", p.info, err)
	}
}
