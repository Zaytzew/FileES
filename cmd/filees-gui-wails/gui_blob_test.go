package main

import (
	"context"
	"filees/pkg/guiblob"
	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
	"strings"
	"testing"
)

type guiBlobClientFunc func(context.Context, string, *guiblob.Write) (guiblob.State, error)

func (f guiBlobClientFunc) GUIBlob(c context.Context, s string, w *guiblob.Write) (guiblob.State, error) {
	return f(c, s, w)
}
func TestGUIBlobRejectsReplacedRealm(t *testing.T) {
	realm := uuid.NewString()
	s := &GUIService{snapshot: Snapshot{Connected: true, Servers: []ServerProjection{{ID: "s", RealmID: realm}}}}
	s.guiBlobs = guiBlobClientFunc(func(context.Context, string, *guiblob.Write) (guiblob.State, error) {
		s.mu.Lock()
		s.snapshot.Servers = []ServerProjection{{ID: "s", RealmID: uuid.NewString()}}
		s.mu.Unlock()
		return guiblob.State{RealmID: realm}, nil
	})
	if _, err := s.GetGUIBlob("s"); err == nil {
		t.Fatal("late reply crossed realm replacement")
	}
}

func TestGUIBlobFrontendBindings(t *testing.T) {
	_ = application.New(application.Options{})
	bindings := application.NewBindings(nil, nil)
	if err := bindings.Add(application.NewService(&GUIService{})); err != nil {
		t.Fatal(err)
	}
	source, err := frontend.ReadFile("frontend/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GetGUIBlob", "SetGUIBlob"} {
		name := "filees/cmd/filees-gui-wails.GUIService." + method
		if bindings.Get(&application.CallOptions{MethodName: name}) == nil {
			t.Fatal("binding missing", name)
		}
		if !strings.Contains(string(source), `Call.ByName("`+name+`"`) {
			t.Fatal("frontend calls wrong binding", name)
		}
	}
}
