package main

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// singleInstanceID names the lock that keeps one interface per login session.
//
// It is a mutex name on Windows and a socket elsewhere, so it has to be stable
// across releases: change it and an old client and a new one stop seeing each
// other, which is the one situation this guard exists to prevent.
const singleInstanceID = "pl.filees.desktop-client"

// raiseMainPanel is set once the main window exists.
//
// The single-instance callback is handed to application.New, which runs before
// any window is constructed, so the callback cannot close over the window
// directly. It closes over this instead and does nothing until the window is
// there - a click that arrives during the first two seconds of startup is
// dropped rather than dereferencing a window that does not exist yet.
var raiseMainPanel struct {
	sync.Mutex
	show func()
}

func setMainPanelRaiser(show func()) {
	raiseMainPanel.Lock()
	defer raiseMainPanel.Unlock()
	raiseMainPanel.show = show
}

// singleInstanceOptions makes a second launch raise the running panel.
//
// Without it the Start Menu and desktop shortcuts were empty handlers: clicking
// one started a whole second client - own tray icon, own daemon connection, own
// hidden window - and showed nothing at all, because this interface starts
// hidden and is raised from the tray. Measured on the owner's machine: one
// click, two processes, one of them with no window.
func singleInstanceOptions() *application.SingleInstanceOptions {
	return &application.SingleInstanceOptions{
		UniqueID: singleInstanceID,
		OnSecondInstanceLaunch: func(_ application.SecondInstanceData) {
			raiseMainPanel.Lock()
			show := raiseMainPanel.show
			raiseMainPanel.Unlock()
			if show != nil {
				show()
			}
		},
	}
}

// handoverEnv carries the pid of the instance a restart is replacing.
//
// The tray's "Uruchom FileES ponownie" starts the replacement from inside the
// process it replaces: on Windows there is no exec that swaps the image, so for
// a moment both are alive. To the single-instance lock that looks exactly like
// a second click on the shortcut, and the restarted interface would exit at
// once, leaving the owner with a tray icon that had quietly stopped existing.
//
// So the replacement is told whom it is replacing and waits for that process to
// go before it takes the lock.
const handoverEnv = "FILEES_GUI_REPLACES_PID"
