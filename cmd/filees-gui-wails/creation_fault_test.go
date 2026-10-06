package main

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
)

type creationFaultClient struct {
	result *contract.RepoLifecycleResult
	polls  int
}

func (c *creationFaultClient) RepoCreateRequest(context.Context, contract.RepoCreateRequestPayload) (*contract.RepoLifecycleResult, error) {
	return c.result, nil
}

func (c *creationFaultClient) RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error) {
	c.polls++
	return c.result, nil
}

func TestCreationFaultPreservesDomainKeyWithOnePoll(t *testing.T) {
	c := &creationFaultClient{result: &contract.RepoLifecycleResult{State: "repository_created", LastErrorKey: "repo.initial_content_unavailable", LastError: "fixture diagnostic"}}
	state, key, detail, progress, err := (repositoryCreateAdapter{client: c}).CreationFault(t.Context(), "op")
	if err != nil || state != c.result.State || key != c.result.LastErrorKey || detail != c.result.LastError || progress != nil || c.polls != 1 {
		t.Fatalf("lost domain fault: %s %s %s %+v %v polls=%d", state, key, detail, progress, err, c.polls)
	}
}
