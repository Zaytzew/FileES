package main

import (
	"context"
	"errors"
	"filees/internal/gui/actions"
	contract "filees/pkg/contract/v1"
	"testing"
)

type conversionBridgeClient struct {
	payload contract.WCConversionPayload
	result  *contract.WCConversionStatus
	err     error
}

func (*conversionBridgeClient) RepoRelocate(context.Context, contract.RepoRelocatePayload) (*contract.RepoLifecycleResult, error) {
	return nil, nil
}
func (*conversionBridgeClient) RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error) {
	return nil, nil
}
func (c *conversionBridgeClient) WCConversion(_ context.Context, p contract.WCConversionPayload) (*contract.WCConversionStatus, error) {
	c.payload = p
	return c.result, c.err
}

func TestConversionBridgeMapsOnlyGUIBoundaryValues(t *testing.T) {
	for _, action := range []string{"begin", "retry", "rollback"} {
		request := actions.WCConversionRequest{ServerID: "lab", RepoID: "repo", Action: action, SourcePath: "confirmed", OperationID: "exact"}
		client := &conversionBridgeClient{result: &contract.WCConversionStatus{ServerID: "lab", RepoID: "repo", SourcePath: "confirmed", OperationID: "exact"}}
		receipt, err := (repositoryRelocateAdapter{client: client}).WCConversion(t.Context(), request)
		want := contract.WCConversionPayload{ServerID: request.ServerID, RepoID: request.RepoID, Action: request.Action, SourcePath: request.SourcePath, OperationID: request.OperationID}
		if err != nil || client.payload != want || receipt == nil || *receipt != (actions.WCConversionReceipt{ServerID: "lab", RepoID: "repo", SourcePath: "confirmed", OperationID: "exact"}) {
			t.Fatal(action, client.payload, receipt, err)
		}
	}
	client := &conversionBridgeClient{}
	if got, err := (repositoryRelocateAdapter{client: client}).WCConversion(t.Context(), actions.WCConversionRequest{}); got != nil || err != nil {
		t.Fatal(got, err)
	}
	client.err = errors.New("lost reply")
	if got, err := (repositoryRelocateAdapter{client: client}).WCConversion(t.Context(), actions.WCConversionRequest{}); got != nil || !errors.Is(err, client.err) {
		t.Fatal(got, err)
	}
}
