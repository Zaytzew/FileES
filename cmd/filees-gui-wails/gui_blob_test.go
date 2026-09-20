package main

import (
	"context"
	"filees/pkg/guiblob"
	"fmt"
	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
	"hash/fnv"
	"regexp"
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

// A go test main package has its import path in reflection, while the built
// executable uses "main". Looking up the test FQN alone misses broken JS calls.
func TestGUIBlobFrontendBindingUsesBuildContextIDs(t *testing.T) {
	_ = application.New(application.Options{})
	bindings := application.NewBindings(nil, nil)
	if err := bindings.Add(application.NewService(&GUIService{})); err != nil {
		t.Fatal(err)
	}
	source, err := frontend.ReadFile("frontend/app.js")
	if err != nil {
		t.Fatal(err)
	}
	module, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/guiservice.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GetGUIBlob", "SetGUIBlob"} {
		name := "filees/cmd/filees-gui-wails.GUIService." + method
		if bindings.Get(&application.CallOptions{MethodName: name}) == nil {
			t.Fatal("binding missing", name)
		}
		hash := fnv.New32a()
		_, _ = hash.Write([]byte("main.GUIService." + method))
		pattern := fmt.Sprintf(`export function %s\([^)]*\)\s*\{\s*return \$Call\.ByID\(%d(?:,|\))`, method, hash.Sum32())
		if !regexp.MustCompile(pattern).Match(module) {
			t.Errorf("frontend lacks %s with executable binding ID %d", method, hash.Sum32())
		}
		if !strings.Contains(string(source), "GUIService."+method+"(") {
			t.Errorf("frontend does not use the %s binding module", method)
		}
	}
}
