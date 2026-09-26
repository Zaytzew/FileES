package mobileworker

import (
	"context"
	"errors"
	"testing"

	"filees/pkg/guiblob"
)

type fakeDrawerReader struct {
	state guiblob.State
	err   error
}

func (f fakeDrawerReader) Read(context.Context, string) (guiblob.State, error) { return f.state, f.err }

func TestParseDrawerDocumentEmptyIsNotAnError(t *testing.T) {
	doc, err := parseDrawerDocument("")
	if err != nil {
		t.Fatalf("no drawers is the ordinary state: %v", err)
	}
	if len(doc.Drawers) != 0 || len(doc.Repos) != 0 {
		t.Fatalf("expected empty document, got %+v", doc)
	}
}

func TestParseDrawerDocumentValid(t *testing.T) {
	doc, err := parseDrawerDocument(`{"schema":"filees.gui.drawers/v1","drawers":[{"id":"d-1","name":"Archiwum"}],"repos":{"repo-1":"d-1"}}`)
	if err != nil {
		t.Fatalf("valid document: %v", err)
	}
	if len(doc.Drawers) != 1 || doc.Drawers[0].ID != "d-1" || doc.Repos["repo-1"] != "d-1" {
		t.Fatalf("unexpected document: %+v", doc)
	}
}

func TestParseDrawerDocumentRejectsWhatTheGUIWouldRefuse(t *testing.T) {
	cases := map[string]string{
		"wrong schema":        `{"schema":"other","drawers":[],"repos":{}}`,
		"dangling assignment": `{"schema":"filees.gui.drawers/v1","drawers":[],"repos":{"repo-1":"missing"}}`,
		"duplicate id":        `{"schema":"filees.gui.drawers/v1","drawers":[{"id":"d-1","name":"A"},{"id":"d-1","name":"B"}],"repos":{}}`,
		"bad id charset":      `{"schema":"filees.gui.drawers/v1","drawers":[{"id":"has space","name":"A"}],"repos":{}}`,
		"blank name":          `{"schema":"filees.gui.drawers/v1","drawers":[{"id":"d-1","name":"  "}],"repos":{}}`,
		"unknown field":       `{"schema":"filees.gui.drawers/v1","drawers":[],"repos":{},"extra":1}`,
		"not json":            `not json`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDrawerDocument(data); err == nil {
				t.Fatalf("%s: expected rejection", name)
			}
		})
	}
}

func TestBrowserListDrawersUnavailableWithoutStore(t *testing.T) {
	b := Browser{Authority: fakeAuthority{access: "r"}}
	if _, err := b.ListDrawers(context.Background(), "client-1"); !errors.Is(err, ErrDrawersUnavailable) {
		t.Fatalf("expected ErrDrawersUnavailable, got %v", err)
	}
}

func TestBrowserListDrawersEmptyRealmIsSuccess(t *testing.T) {
	b := Browser{Authority: fakeAuthority{access: "r"}, Drawers: fakeDrawerReader{state: guiblob.State{}}}
	res, err := b.ListDrawers(context.Background(), "client-1")
	if err != nil {
		t.Fatalf("empty drawer state should not be an error: %v", err)
	}
	if len(res.Drawers) != 0 || len(res.Assignments) != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

func TestBrowserListDrawersReturnsProjection(t *testing.T) {
	state := guiblob.State{
		Version: "5b2b2595-312c-4e8f-9407-148e2a174034",
		Data:    `{"schema":"filees.gui.drawers/v1","drawers":[{"id":"d-1","name":" Archiwum "}],"repos":{"repo-1":"d-1"}}`,
	}
	b := Browser{Authority: fakeAuthority{access: "r"}, Drawers: fakeDrawerReader{state: state}}
	res, err := b.ListDrawers(context.Background(), "client-1")
	if err != nil {
		t.Fatalf("ListDrawers: %v", err)
	}
	if res.Version != state.Version {
		t.Fatalf("version not forwarded: %+v", res)
	}
	if len(res.Drawers) != 1 || res.Drawers[0].ID != "d-1" || res.Drawers[0].Name != "Archiwum" {
		t.Fatalf("drawer not projected/trimmed: %+v", res.Drawers)
	}
	if res.Assignments["repo-1"] != "d-1" {
		t.Fatalf("assignment not projected: %+v", res.Assignments)
	}
}

func TestBrowserListDrawersPropagatesReaderError(t *testing.T) {
	boom := errors.New("boom")
	b := Browser{Authority: fakeAuthority{access: "r"}, Drawers: fakeDrawerReader{err: boom}}
	if _, err := b.ListDrawers(context.Background(), "client-1"); !errors.Is(err, boom) {
		t.Fatalf("expected reader error to propagate, got %v", err)
	}
}
