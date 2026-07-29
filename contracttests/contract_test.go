package contracttests

import (
	"encoding/json"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestRequestValidate(t *testing.T) {
	valid := contract.Request{
		Protocol:  contract.Protocol,
		RequestID: "request-1",
		ClientID:  "test-client",
		Command:   contract.CmdSystemHello,
		Payload:   json.RawMessage(`{}`),
	}

	tests := []struct {
		name    string
		mutate  func(*contract.Request)
		wantErr string
	}{
		{name: "valid"},
		{name: "unsupported protocol", mutate: func(r *contract.Request) { r.Protocol = "filees.contract/v2" }, wantErr: "unsupported protocol"},
		{name: "missing request id", mutate: func(r *contract.Request) { r.RequestID = " " }, wantErr: "missing request_id"},
		{name: "missing client id", mutate: func(r *contract.Request) { r.ClientID = "" }, wantErr: "missing client_id"},
		{name: "missing command", mutate: func(r *contract.Request) { r.Command = "\t" }, wantErr: "missing command"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			err := req.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestRequestJSONGolden(t *testing.T) {
	req := contract.Request{
		Protocol:  contract.Protocol,
		RequestID: "request-1",
		ClientID:  "fileesctl",
		Command:   contract.CmdRepoStatus,
		RepoID:    "projectA",
		Payload:   json.RawMessage(`{}`),
	}

	got, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"protocol":"filees.contract/v1","request_id":"request-1","client_id":"fileesctl","command":"repo.status","repo_id":"projectA","payload":{}}`
	if string(got) != want {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestDecodePayloadIgnoresUnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"repo_id":"projectA","limit":7,"future_field":{"enabled":true}}`)
	var payload contract.ErrorListPayload
	if err := contract.DecodePayload(raw, &payload); err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if payload.RepoID != "projectA" || payload.Limit != 7 {
		t.Fatalf("decoded payload = %#v", payload)
	}
}

func TestActivationPayloadRoundTrip(t *testing.T) {
	want := contract.ActivationFinishPayload{ServerID: "office", ServerAddress: "filees.example.net:22", KnownHostsPath: "/state/known_hosts", StateRoot: "/state/activation", RemotePort: 42000, OTP: contract.Secret("OTP-CODE")}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got contract.ActivationFinishPayload
	if err := contract.DecodePayload(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ServerID != want.ServerID || got.ServerAddress != want.ServerAddress || got.KnownHostsPath != want.KnownHostsPath || got.StateRoot != want.StateRoot || got.RemotePort != want.RemotePort || string(got.OTP) != string(want.OTP) {
		t.Fatalf("payload=%+v", got)
	}
}

func TestMobilePairingBeginRoundTrip(t *testing.T) {
	wantPayload := contract.MobilePairingBeginPayload{ServerID: "biuro"}
	raw, err := json.Marshal(wantPayload)
	if err != nil {
		t.Fatal(err)
	}
	var gotPayload contract.MobilePairingBeginPayload
	if err := contract.DecodePayload(raw, &gotPayload); err != nil {
		t.Fatal(err)
	}
	if gotPayload != wantPayload {
		t.Fatalf("payload=%+v", gotPayload)
	}

	wantResult := contract.MobilePairingBeginResult{Token: "OTP-TOKEN", ExpiresAt: "2026-07-24T12:00:00Z", Address: "filees.example.net:22", HostPublicKey: "ssh-ed25519 AAAA..."}
	resp := contract.OKResponse("request-1", wantResult)
	var gotResult contract.MobilePairingBeginResult
	if err := contract.DecodeResult(resp.Result, &gotResult); err != nil {
		t.Fatal(err)
	}
	if gotResult != wantResult {
		t.Fatalf("result=%+v", gotResult)
	}
}

func TestResponseBuilders(t *testing.T) {
	ok := contract.OKResponse("request-1", contract.RepoListResult{
		Repos: []contract.RepoSummary{{ID: "projectA"}},
	})
	if ok.Protocol != contract.Protocol || ok.RequestID != "request-1" || ok.Status != contract.StatusOK || ok.Error != nil {
		t.Fatalf("invalid OK response: %#v", ok)
	}
	var result contract.RepoListResult
	if err := contract.DecodeResult(ok.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Repos) != 1 || result.Repos[0].ID != "projectA" {
		t.Fatalf("decoded result = %#v", result)
	}

	failure := contract.ErrResponse(
		"request-2",
		"PROTO-0001",
		"ERROR",
		"NONE",
		"proto.invalid",
		map[string]string{"field": "command"},
	)
	if failure.Protocol != contract.Protocol || failure.RequestID != "request-2" || failure.Status != contract.StatusError || failure.Error == nil {
		t.Fatalf("invalid error response: %#v", failure)
	}
	if failure.Error.Code != "PROTO-0001" || failure.Error.Details["field"] != "command" {
		t.Fatalf("invalid error body: %#v", failure.Error)
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	event := contract.NewEvent(
		"event-1",
		42,
		contract.EvRepoStateChanged,
		"projectA",
		contract.RepoStateChangedPayload{
			OldState: contract.StateInitializing,
			NewState: contract.StateActive,
		},
	)
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded contract.Event
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Protocol != contract.Protocol || decoded.EventID != "event-1" || decoded.Sequence != 42 || decoded.Type != contract.EvRepoStateChanged {
		t.Fatalf("round trip event = %#v", decoded)
	}
	var payload contract.RepoStateChangedPayload
	if err := contract.DecodePayload(decoded.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OldState != contract.StateInitializing || payload.NewState != contract.StateActive {
		t.Fatalf("round trip payload = %#v", payload)
	}
}

func TestAdvertisedCapabilitiesMatchImplementedV1Subset(t *testing.T) {
	want := map[string]bool{
		contract.CapEventsSubscribe:        true,
		contract.CapRepoLock:               true,
		contract.CapRepoUnlock:             true,
		contract.CapRepoReservationList:    true,
		contract.CapRepoReservationRelease: true,
		contract.CapErrorList:              true,
		contract.CapActivationBegin:        true,
		contract.CapActivationFinish:       true,
		contract.CapRealmAliasClaim:        true,
		contract.CapServerDetach:           true,
		contract.CapRealmRemoveBegin:       true,
		contract.CapRealmRemoveConfirm:     true,
		contract.CapMobilePairingBegin:     true,
		contract.CapRepoCreateRequest:      true,
		contract.CapRepoAttachIntent:       true,
		contract.CapRepoAttachApprove:      true,
		contract.CapRepoRelocate:           true,
		contract.CapRepoDetach:             true,
		contract.CapRepoDelete:             true,
		contract.CapRepoLifecycleStatus:    true,
	}
	if len(contract.AllCapabilities) != len(want) {
		t.Fatalf("AllCapabilities = %#v", contract.AllCapabilities)
	}
	for _, capability := range contract.AllCapabilities {
		if !want[capability] {
			t.Errorf("unexpected advertised capability %q", capability)
		}
	}
}
