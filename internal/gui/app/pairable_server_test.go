package app

import (
	"testing"

	contract "filees/pkg/contract/v1"
)

// A demo server refuses MOBILE_PAIRING by policy: pairing is offered only
// when another server exists, and then for that server - not for whichever
// happens to be first.
func TestMobilePairingSkipsDemoServers(t *testing.T) {
	demo := ServerViewModel{ID: "demo", DemoExpiresAt: "2026-09-24T21:06:00Z"}
	office := ServerViewModel{ID: "office"}
	vm := ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapMobilePairingBegin: true}, Servers: []ServerViewModel{demo}}
	if vm.CanPairMobile() {
		t.Fatal("pairing offered with only a demo server")
	}
	vm.Servers = []ServerViewModel{demo, office}
	server, ok := vm.PairableServer()
	if !vm.CanPairMobile() || !ok || server.ID != "office" {
		t.Fatalf("pairable = %+v, %v", server, ok)
	}
}
