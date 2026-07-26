package ipcserver

import (
	"testing"

	contract "filees/pkg/contract/v1"
)

type systemLifecycleRecorder struct{ restarts, shutdowns int }

func (recorder *systemLifecycleRecorder) Restart()  { recorder.restarts++ }
func (recorder *systemLifecycleRecorder) Shutdown() { recorder.shutdowns++ }

func TestSystemLifecycleCapabilitiesAreAdvertisedOnlyWhenWired(t *testing.T) {
	server := New("unused")
	if containsCapability(server.capabilities(), contract.CapSystemRestart) ||
		containsCapability(server.capabilities(), contract.CapSystemShutdown) {
		t.Fatal("unwired system lifecycle capabilities were advertised")
	}
	server.SetSystemLifecycleService(&systemLifecycleRecorder{})
	if !containsCapability(server.capabilities(), contract.CapSystemRestart) ||
		!containsCapability(server.capabilities(), contract.CapSystemShutdown) {
		t.Fatalf("wired system lifecycle capabilities=%v", server.capabilities())
	}
}

func TestSystemLifecycleRunsOnlyAfterSuccessfulResponse(t *testing.T) {
	server := New("unused")
	recorder := &systemLifecycleRecorder{}
	server.SetSystemLifecycleService(recorder)
	for _, test := range []struct {
		command string
		action  string
	}{
		{command: contract.CmdSystemRestart, action: "restart"},
		{command: contract.CmdSystemShutdown, action: "shutdown"},
	} {
		beforeRestarts, beforeShutdowns := recorder.restarts, recorder.shutdowns
		response := server.dispatch(contract.Request{RequestID: test.command, Command: test.command})
		if response.Status != contract.StatusOK {
			t.Fatalf("%s response=%+v", test.command, response.Error)
		}
		var result contract.SystemLifecycleResult
		if err := contract.DecodeResult(response.Result, &result); err != nil || result.Action != test.action {
			t.Fatalf("%s result=%+v err=%v", test.command, result, err)
		}
		if recorder.restarts != beforeRestarts || recorder.shutdowns != beforeShutdowns {
			t.Fatal("lifecycle action ran before response acknowledgement")
		}
		server.afterResponse(test.command)
	}
	if recorder.restarts != 1 || recorder.shutdowns != 1 {
		t.Fatalf("lifecycle calls restart=%d shutdown=%d", recorder.restarts, recorder.shutdowns)
	}
}
