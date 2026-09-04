package main

import (
	"os"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// secondInstanceLaunch is the payload Wails hands the callback.
func secondInstanceLaunch() application.SecondInstanceData {
	return application.SecondInstanceData{Args: []string{"filees-gui-wails"}}
}

// A second launch must reach the running panel, not start a client beside it.
//
// Measured on the owner's machine before this existed: clicking the Start Menu
// shortcut while the client was running left two processes, the newer one with
// no window at all - this interface starts hidden and is raised from the tray,
// so a second copy is invisible, keeps its own daemon connection, and looks to
// the person clicking like a shortcut that does nothing.
func TestASecondLaunchRaisesTheRunningPanel(t *testing.T) {
	options := singleInstanceOptions()
	if options == nil {
		t.Fatal("no single instance options; every shortcut click would start another client")
	}
	if strings.TrimSpace(options.UniqueID) == "" {
		t.Fatal("the lock has no name, so it locks nothing")
	}
	if options.OnSecondInstanceLaunch == nil {
		t.Fatal("a second launch is refused and nothing is raised; the click does nothing at all")
	}

	raised := 0
	t.Cleanup(func() { setMainPanelRaiser(nil) })
	setMainPanelRaiser(func() { raised++ })
	options.OnSecondInstanceLaunch(secondInstanceLaunch())
	if raised != 1 {
		t.Fatalf("panel raised %d times; want once", raised)
	}
}

// And a launch during startup must not reach for a window that is not built.
//
// application.New takes the callback before any window exists, so there is a
// window of time - short, but the one a person hits when they double-click
// impatiently - where the only safe answer is to do nothing.
func TestASecondLaunchBeforeTheWindowExistsIsHarmless(t *testing.T) {
	t.Cleanup(func() { setMainPanelRaiser(nil) })
	setMainPanelRaiser(nil)
	singleInstanceOptions().OnSecondInstanceLaunch(secondInstanceLaunch())
}

// The application has to actually declare it. The options above are inert
// unless they are handed to Wails.
func TestSingleInstanceIsHandedToTheApplication(t *testing.T) {
	source := readMainSource(t)
	start := strings.Index(source, "application.New(application.Options{")
	if start < 0 {
		t.Fatal("the application is no longer constructed with application.Options")
	}
	end := strings.Index(source[start:], "\n\t})")
	if end < 0 {
		t.Fatal("could not find the end of the application options")
	}
	if !strings.Contains(source[start:start+end], "SingleInstance:") {
		t.Error("application.Options carries no SingleInstance; shortcut clicks would keep starting extra clients")
	}
	if !strings.Contains(source, "setMainPanelRaiser(") {
		t.Error("nothing tells the callback which window to raise, so a second launch would be silently ignored")
	}
}

func readMainSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
