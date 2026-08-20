package main

import (
	"context"

	contract "filees/pkg/contract/v1"
	whale "filees/pkg/whale/v1"
	"filees/pkg/whaleclient"
)

type whaleIPCService struct {
	manager *whaleclient.Manager
	daemon  context.Context
}

func (s whaleIPCService) List(context.Context) ([]contract.WhaleOperation, error) {
	operations, err := s.manager.List()
	if err != nil {
		return nil, err
	}
	result := make([]contract.WhaleOperation, 0, len(operations))
	for _, operation := range operations {
		result = append(result, projectWhaleOperation(operation))
	}
	return result, nil
}

func (s whaleIPCService) Get(_ context.Context, operationID string) (contract.WhaleOperation, error) {
	operation, err := s.manager.Get(operationID)
	return projectWhaleOperation(operation), err
}

func (s whaleIPCService) BeginPut(_ context.Context, payload contract.WhalePutBeginPayload) (contract.WhaleOperation, error) {
	operation, err := s.manager.BeginPut(s.daemon, payload.ServerID, payload.RepoID, payload.LogicalPath, payload.SourcePath)
	return projectWhaleOperation(operation), err
}

func (s whaleIPCService) BeginGet(_ context.Context, payload contract.WhaleGetBeginPayload) (contract.WhaleOperation, error) {
	if payload.GenerationID == "" && payload.ExpectedSize == 0 && payload.SHA256 == "" {
		operation, err := s.manager.BeginGetTarget(s.daemon, payload.ServerID, payload.RepoID, payload.LogicalPath, payload.Revision, payload.DestinationPath)
		return projectWhaleOperation(operation), err
	}
	identity := whale.Identity{LogicalRepoID: payload.RepoID, LogicalPath: payload.LogicalPath, GenerationID: payload.GenerationID, ExpectedSize: payload.ExpectedSize, SHA256: payload.SHA256}
	operation, err := s.manager.BeginGet(s.daemon, payload.ServerID, identity, payload.Revision, payload.DestinationPath)
	return projectWhaleOperation(operation), err
}

func (s whaleIPCService) ConfirmGet(_ context.Context, operationID string) (contract.WhaleOperation, error) {
	operation, err := s.manager.ConfirmGet(s.daemon, operationID)
	return projectWhaleOperation(operation), err
}

func (s whaleIPCService) Retry(_ context.Context, operationID string) (contract.WhaleOperation, error) {
	operation, err := s.manager.Retry(s.daemon, operationID)
	return projectWhaleOperation(operation), err
}

func (s whaleIPCService) Cancel(_ context.Context, operationID string, removePayload bool) (contract.WhaleOperation, error) {
	operation, err := s.manager.Cancel(operationID, removePayload)
	return projectWhaleOperation(operation), err
}

func projectWhaleOperation(operation whaleclient.Operation) contract.WhaleOperation {
	identity := contract.WhaleIdentity{LogicalRepoID: operation.LogicalRepoID, LogicalPath: operation.LogicalPath, GenerationID: operation.GenerationID}
	if operation.Identity != nil {
		identity.ExpectedSize = operation.Identity.ExpectedSize
		identity.SHA256 = operation.Identity.SHA256
	}
	return contract.WhaleOperation{OperationID: operation.OperationID, ServerID: operation.ServerID, Direction: string(operation.Direction), Identity: identity, Revision: operation.Revision, SourcePath: operation.SourcePath, DestinationPath: operation.DestinationPath, State: string(operation.State), BytesHave: operation.BytesHave, PublishedRevision: operation.PublishedRevision, LastError: operation.LastError, CreatedAt: operation.CreatedAt, UpdatedAt: operation.UpdatedAt}
}
