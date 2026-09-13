package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
)

// QuiesceRequests closes ingress and waits for admitted handlers. Drain this
// BEFORE OperationAdmission: an admitted handler may still need a worker.
// Resume on aborted restart preparation. This is not a global safe point.
func (s *Server) QuiesceRequests(ctx context.Context) (func(), error) {
	return s.requestAdmission.Quiesce(ctx)
}

func (s *Server) admitRequest(command string) (func(), error) {
	// Only discovery, catalogue and read-only system status stay available.
	// Unknown future commands are fenced. Event subscriptions use handleConn.
	switch command {
	case contract.CmdSystemHello, contract.CmdSystemStatus, contract.CmdMessagesCatalog:
		return func() {}, nil
	default:
		return s.requestAdmission.Enter()
	}
}
