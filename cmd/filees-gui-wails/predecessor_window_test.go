package main

import (
	"errors"
	"testing"
)

func TestReplacePredecessorOffersToKeepSettingsByDefault(t *testing.T) {
	for _, from := range []string{"msi", "store"} {
		p := &initialChannelPromptStub{choice: PromptSelectResult{Value: "keep"}}
		chosen := ""
		if err := replacePredecessor(t.Context(), p, from, func(settings string) error { chosen = settings; return nil }); err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		if chosen != "keep" {
			t.Fatalf("%s: replaced with settings %q", from, chosen)
		}
		r := p.request
		if r.PresentationKey != "select.replacePredecessor" || r.Default != "keep" || len(r.Options) != 2 {
			t.Fatalf("%s: request = %+v", from, r)
		}
		if r.PresentationArgs["previous"] != predecessorNames[from] {
			t.Fatalf("%s: dialog names %q", from, r.PresentationArgs["previous"])
		}
	}
}

// Cancel, an unknown answer or an unknown variant must never reach the
// command that uninstalls.
func TestReplacePredecessorDoesNothingWithoutAConfirmedChoice(t *testing.T) {
	for name, c := range map[string]struct {
		from   string
		choice PromptSelectResult
	}{
		"cancel":          {"msi", PromptSelectResult{Cancelled: true}},
		"unknown answer":  {"msi", PromptSelectResult{Value: "everything"}},
		"unknown variant": {"winget", PromptSelectResult{Value: "keep"}},
	} {
		t.Run(name, func(t *testing.T) {
			p := &initialChannelPromptStub{choice: c.choice}
			called := false
			if err := replacePredecessor(t.Context(), p, c.from, func(string) error { called = true; return nil }); err == nil {
				t.Fatal("no error")
			}
			if called {
				t.Fatal("replacement ran without a confirmed choice")
			}
		})
	}
}

// A failed replacement is shown, with its reason, and reported as a failure,
// so the launcher does not go on to start a second pair next to the first.
func TestReplacePredecessorFailureIsShownAndNotSuccess(t *testing.T) {
	p := &initialChannelPromptStub{choice: PromptSelectResult{Value: "default"}}
	failure := errors.New("msiexec /x: exit status 1603")
	err := replacePredecessor(t.Context(), p, "msi", func(string) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}
	if p.info.PresentationKey != "info.replacePredecessorFailed" || p.info.PresentationArgs["reason"] != failure.Error() {
		t.Fatalf("info = %+v", p.info)
	}
}
