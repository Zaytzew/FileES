package ipcserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"time"

	contract "filees/pkg/contract/v1"
)

const maxFrameBytes = 4 * 1024 * 1024 // 4 MiB max single JSON frame

// These bound transport inactivity, not command execution. A slow worker may
// still finish normally; a peer that stops sending/reading cannot retain a
// connection and its frame buffer indefinitely. Event streams clear read expiry.
const connectionIOTimeout = 30 * time.Second
const initialFrameBytes = 4 * 1024

// handleConn drives one client connection: reads JSON Lines, dispatches commands,
// writes responses. Switches to event-streaming mode on events.subscribe.
func (s *Server) handleConn(c net.Conn) {
	defer c.Close()

	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, initialFrameBytes), maxFrameBytes)
	enc := json.NewEncoder(c)
	write := func(value any) error {
		if err := c.SetWriteDeadline(time.Now().Add(connectionIOTimeout)); err != nil {
			return err
		}
		return enc.Encode(value)
	}

	for {
		if err := c.SetReadDeadline(time.Now().Add(connectionIOTimeout)); err != nil {
			return
		}
		if !sc.Scan() {
			return
		}
		if err := c.SetReadDeadline(time.Time{}); err != nil {
			return
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var req contract.Request
		if err := json.Unmarshal(line, &req); err != nil {
			if err := write(protoErr("", "proto.parse_error",
				map[string]string{"detail": err.Error()})); err != nil {
				return
			}
			continue
		}
		if err := req.Validate(); err != nil {
			if err := write(protoErr(req.RequestID, "proto.invalid_envelope",
				map[string]string{"detail": err.Error()})); err != nil {
				return
			}
			continue
		}

		// events.subscribe switches connection to push mode for its lifetime
		if req.Command == contract.CmdEventsSubscribe {
			s.streamEvents(req, c, enc, sc)
			return
		}

		resp := s.dispatch(req)
		if err := write(resp); err != nil {
			return
		}
		if resp.Status == contract.StatusOK {
			s.afterResponse(req.Command)
		}
	}
}

// afterResponse performs process-lifecycle actions only after their
// acknowledgement is on the wire. Closing the daemon context from the handler
// itself could tear down the request connection before the GUI learns that the
// command was accepted.
func (s *Server) afterResponse(command string) {
	service := s.systemLifecycleService()
	if service == nil {
		return
	}
	switch command {
	case contract.CmdSystemRestart:
		service.Restart()
	case contract.CmdSystemShutdown:
		service.Shutdown()
	}
}

// streamEvents registers this connection as an event subscriber, sends an ok
// response, then forwards broadcast events until the connection closes.
func (s *Server) streamEvents(req contract.Request, c net.Conn, enc *json.Encoder, sc *bufio.Scanner) {
	ch := make(chan contract.Event, 64)
	s.addSub(ch)
	defer s.removeSub(ch)

	// acknowledge the subscribe command
	if err := c.SetWriteDeadline(time.Now().Add(connectionIOTimeout)); err != nil {
		return
	}
	if err := enc.Encode(contract.OKResponse(req.RequestID, map[string]bool{"streaming": true})); err != nil {
		return
	}

	// detect disconnection: drain scanner in a goroutine
	disconnected := make(chan struct{})
	go func() {
		for sc.Scan() {
		} // blocks until EOF / error
		close(disconnected)
	}()

	for {
		select {
		case ev := <-ch:
			if err := c.SetWriteDeadline(time.Now().Add(connectionIOTimeout)); err != nil {
				return
			}
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
