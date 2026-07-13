package ipcserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"

	contract "filees/pkg/contract/v1"
)

const maxFrameBytes = 4 * 1024 * 1024 // 4 MiB max single JSON frame

// handleConn drives one client connection: reads JSON Lines, dispatches commands,
// writes responses. Switches to event-streaming mode on events.subscribe.
func (s *Server) handleConn(c net.Conn) {
	defer c.Close()

	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, maxFrameBytes), maxFrameBytes)
	enc := json.NewEncoder(c)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var req contract.Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(protoErr("", "proto.parse_error",
				map[string]string{"detail": err.Error()}))
			continue
		}
		if err := req.Validate(); err != nil {
			_ = enc.Encode(protoErr(req.RequestID, "proto.invalid_envelope",
				map[string]string{"detail": err.Error()}))
			continue
		}

		// events.subscribe switches connection to push mode for its lifetime
		if req.Command == contract.CmdEventsSubscribe {
			s.streamEvents(req, c, enc, sc)
			return
		}

		resp := s.dispatch(req)
		_ = enc.Encode(resp)
	}
}

// streamEvents registers this connection as an event subscriber, sends an ok
// response, then forwards broadcast events until the connection closes.
func (s *Server) streamEvents(req contract.Request, c net.Conn, enc *json.Encoder, sc *bufio.Scanner) {
	ch := make(chan contract.Event, 64)
	s.addSub(ch)
	defer s.removeSub(ch)

	// acknowledge the subscribe command
	_ = enc.Encode(contract.OKResponse(req.RequestID, map[string]bool{"streaming": true}))

	// detect disconnection: drain scanner in a goroutine
	disconnected := make(chan struct{})
	go func() {
		for sc.Scan() {} // blocks until EOF / error
		close(disconnected)
	}()

	for {
		select {
		case ev := <-ch:
			if err := enc.Encode(ev); err != nil {
				return
			}
		case <-disconnected:
			return
		}
	}
}

// protoErr builds a Response for internal protocol-level errors (no errmap code).
func protoErr(requestID, msgKey string, details map[string]string) contract.Response {
	return contract.ErrResponse(requestID, "PROTO-0001", "ERROR", "NONE", msgKey, details)
}
