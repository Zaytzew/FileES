package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
)

// QuiesceRequests closes ingress and waits for admitted requests, including
// response writes and their lifecycle callbacks. Drain this
// BEFORE OperationAdmission: an admitted handler may still need a worker.
// Resume on aborted restart preparation. This is not a global safe point.
func (s *Server) QuiesceRequests(ctx context.Context) (func(), error) {
	return s.requestAdmission.Quiesce(ctx)
}

// executeRequest holds one lease through the transport acknowledgement and
// lifecycle callback. Never enter again in dispatch: a concurrent drain may
// have closed ingress while this request was already accepted.
func (s *Server) executeRequest(req contract.Request, write func(any) error) error {
	release, err := s.admitRequest(req.Command)
	if err != nil {
		return write(contract.ErrResponseFrom(req.RequestID, errcat.New("system.quiescing", nil, err)))
	}
	defer release()
	resp := s.dispatchAdmitted(req)
	if err := write(resp); err != nil {
		return err
	}
	if resp.Status == contract.StatusOK {
		s.afterResponse(req.Command)
	}
	return nil
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
