package contracttests

import (
	"encoding/json"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/pkg/ipcclient"
)

func TestPublicShareSizeBudgetsAndIPCPreflight(t *testing.T) {
	if control.TicketByteLimit(control.TicketCreatePublicShare) != 2<<20 || control.TicketByteLimit(control.TicketUpdatePublicShare) != 2<<20 || control.ResultByteLimit(control.TicketListPublicShares) != 16<<20 {
		t.Fatal("public share wire budgets changed")
	}
	if control.TicketByteLimit(control.TicketListPublicShares) != 64<<10 || control.ResultByteLimit(control.TicketCreatePublicShare) != 64<<10 || control.TicketByteLimit(control.TicketCreateRepository) != 64<<10 {
		t.Fatal("ordinary operations received larger budgets")
	}
	for _, command := range []string{contract.CmdRepoPublicShareCreate, contract.CmdRepoPublicShareUpdate} {
		payload, _ := json.Marshal(map[string]string{"oversized": strings.Repeat("a", 4<<20)})
		request := contract.Request{RequestID: "size-test", Command: command, Payload: payload}
		response, err := ipcclient.New("must-not-be-opened", "test").Do(t.Context(), request)
		if err != nil || response.Error == nil || response.Error.MessageKey != "public_share.request_too_large" || response.RequestID != request.RequestID {
			t.Fatalf("IPC preflight: %+v / %v", response, err)
		}
	}
}
