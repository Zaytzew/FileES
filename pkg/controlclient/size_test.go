package controlclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func sizeTestPeer(t *testing.T, respond func(ssh.Channel, control.Ticket)) *Client {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(signer.PublicKey().Marshal()) {
				return nil, errors.New("wrong key")
			}
			return nil, nil
		}}
		cfg.AddHostKey(signer)
		server, channels, requests, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			defer channel.Close()
			request := <-requests
			if request == nil || request.Type != "exec" {
				return
			}
			_ = request.Reply(true, nil)
			go ssh.DiscardRequests(requests)
			raw, err := io.ReadAll(channel)
			if err != nil {
				return
			}
			ticket, err := control.ParseTicket(raw)
			if err != nil {
				return
			}
			respond(channel, ticket)
			// Finish the SSH channel, but keep TCP alive until the client has
			// drained the reply and closed its connection. Closing TCP here
			// races the client's window-adjust packets on large responses.
			_ = channel.Close()
			_ = server.Wait()
			return
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return &Client{address: listener.Addr().String(), timeout: 5 * time.Second, signer: signer, hostKeys: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if string(key.Marshal()) != string(signer.PublicKey().Marshal()) {
			return errors.New("wrong host")
		}
		return nil
	}}
}

func sizeListTicket(t *testing.T) control.Ticket {
	t.Helper()
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketListPublicShares, uuid.NewString(), control.ListPublicSharesPayload{RepoID: uuid.NewString()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestPublicShareLargeListingOverSSH(t *testing.T) {
	client := sizeTestPeer(t, func(channel ssh.Channel, ticket control.Ticket) {
		var payload control.ListPublicSharesPayload
		_ = control.DecodePayload(ticket.Payload, &payload)
		share := control.PublicShareSummary{ChannelID: uuid.NewString(), RepoID: payload.RepoID, Alias: "pracownia", Slug: "archiwum", SourceRoot: ".", State: "active", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		for i := 0; i < 4096; i++ {
			name := fmt.Sprintf("archiwum/Załącznik-%04d.dwg", i)
			share.Objects = append(share.Objects, control.PublicShareObject{PublicID: fmt.Sprintf("%016x", i), RepoPath: name, DisplayName: name})
		}
		result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ListPublicSharesResult{Shares: []control.PublicShareSummary{share}}, time.Now())
		if err != nil {
			return
		}
		_ = json.NewEncoder(channel).Encode(result)
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	})
	result, err := client.Exchange(t.Context(), sizeListTicket(t))
	if err != nil {
		t.Fatal(err)
	}
	var list control.ListPublicSharesResult
	if err := control.DecodePayload(result.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Shares) != 1 || len(list.Shares[0].Objects) != 4096 {
		t.Fatal("listing truncated")
	}
}

func TestOversizedSSHResponseClosesBeforeWaitingForExit(t *testing.T) {
	for _, kind := range []control.TicketType{control.TicketListPublicShares, control.TicketClientDeactivate} {
		t.Run(string(kind), func(t *testing.T) {
			client := sizeTestPeer(t, func(channel ssh.Channel, _ control.Ticket) {
				// More than a flow-control window remains unread. No exit status.
				_, _ = io.Copy(channel, strings.NewReader(strings.Repeat("x", control.ResultByteLimit(kind)+(4<<20))))
			})
			ticket := sizeListTicket(t)
			if kind == control.TicketClientDeactivate {
				ticket, _ = control.NewTicket(ticket.OperationID, ticket.RequestID, kind, ticket.ClientID, control.ClientDeactivatePayload{}, time.Now())
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err := client.Exchange(ctx, ticket)
			if !errors.Is(err, control.ErrResultTooLarge) || ctx.Err() != nil {
				t.Fatalf("expected prompt size error, got %v, context %v", err, ctx.Err())
			}
		})
	}
}

func TestServerTicketSizeRejectionIsTyped(t *testing.T) {
	client := sizeTestPeer(t, func(channel ssh.Channel, _ control.Ticket) {
		_, _ = io.WriteString(channel.Stderr(), "repository worker: control ticket exceeds limit\n")
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{70}))
	})
	_, err := client.Exchange(t.Context(), sizeListTicket(t))
	if !errors.Is(err, control.ErrTicketTooLarge) {
		t.Fatalf("size cause lost: %v", err)
	}
}

func TestOversizedTicketRejectedBeforeDial(t *testing.T) {
	declaration := control.PublicShareDeclaration{RepoID: uuid.NewString(), SourceRoot: ".", Slug: "archiwum", Objects: []control.PublicShareObject{{PublicID: "0123456789abcdef", RepoPath: strings.Repeat("a", control.MaxShareTicketBytes), DisplayName: "file"}}}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketCreatePublicShare, uuid.NewString(), control.CreatePublicSharePayload{PublicShareDeclaration: declaration}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Client{}).Exchange(t.Context(), ticket)
	if !errors.Is(err, control.ErrTicketTooLarge) {
		t.Fatalf("expected preflight rejection: %v", err)
	}
}
