package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestPairingBridgeExposesOnlyPresentationControls(t *testing.T) {
	_ = application.New(application.Options{})
	bindings := application.NewBindings(nil, nil)
	if err := bindings.Add(application.NewService(newPairingBridge(newPairingService()))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"filees/cmd/filees-gui-wails.PairingBridge.Snapshot",
		"filees/cmd/filees-gui-wails.PairingBridge.Close",
		"filees/cmd/filees-gui-wails.PairingBridge.Cancel",
	} {
		if bindings.Get(&application.CallOptions{MethodName: name}) == nil {
			t.Fatalf("named pairing binding not found: %s", name)
		}
	}
	if bindings.Get(&application.CallOptions{MethodName: "filees/cmd/filees-gui-wails.PairingService.Present"}) != nil {
		t.Fatal("Go-only QR presentation method was exposed to the WebView")
	}
}

func TestPairingServiceClearsSecretSnapshotWhenClosed(t *testing.T) {
	service := newPairingService()
	service.displayGrace = time.Hour
	shown := make(chan struct{}, 1)
	hidden := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() { hidden <- struct{}{} })

	result := make(chan error, 1)
	go func() {
		result <- service.Present(context.Background(), pairingPresentation{
			Address: "spot:2223", QRDataURL: "data:image/png;base64,c2VjcmV0",
			ExpiresAt: time.Now().Add(time.Hour),
		})
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("pairing window was not shown")
	}
	snapshot := service.Snapshot()
	if !snapshot.Active || !strings.Contains(snapshot.QRDataURL, "c2VjcmV0") || snapshot.Revision == 0 {
		t.Fatalf("unexpected active pairing snapshot: %+v", snapshot)
	}
	if accepted := service.Close(PairingClose{Revision: snapshot.Revision}); !accepted.Accepted {
		t.Fatalf("pairing close rejected: %+v", accepted)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pairing presentation did not close")
	}
	select {
	case <-hidden:
	case <-time.After(time.Second):
		t.Fatal("pairing window was not hidden")
	}
	cleared := service.Snapshot()
	if cleared.Active || cleared.QRDataURL != "" || cleared.Revision <= snapshot.Revision {
		t.Fatalf("pairing secret survived close: %+v", cleared)
	}
}

func TestPairingFrontendClearsQRAndUsesNativeWindow(t *testing.T) {
	source, err := frontend.ReadFile("frontend/pairing.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"PairingService.Snapshot()", "PairingService.Close", "removeAttribute(\"src\")", "filees:pairing-snapshot"} {
		if !strings.Contains(string(source), wanted) {
			t.Fatalf("native pairing frontend missing %q", wanted)
		}
	}
	for _, forbidden := range []string{"http://127.0.0.1", "xdg-open", "window.open("} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("native pairing frontend contains legacy browser path %q", forbidden)
		}
	}
}

func TestPairingFrontendBindingUsesBuildContextIDs(t *testing.T) {
	index, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/index.js")
	if err != nil || !strings.Contains(string(index), "PairingService") {
		t.Fatalf("pairing binding missing from frontend index: %v", err)
	}
	module, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/pairingservice.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ByID(288208043", "ByID(358808385", "ByID(1746464029"} {
		if !strings.Contains(string(module), id) {
			t.Fatalf("pairing service binding missing build-context ID %s", id)
		}
	}
}
