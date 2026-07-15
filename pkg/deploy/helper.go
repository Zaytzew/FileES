package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const HelperUser = "filees-worker"

type HelperConfig struct {
	OperationID string
	ClientID    string
	ListenAddr  string
	WorkerKey   ssh.PublicKey
	HostSigner  ssh.Signer
	Identity    InstallationIdentityGenerator
}

type InstallationIdentityGenerator interface {
	GenerateInstallationIdentity(operationID, clientID string) (Identity, error)
}

type HelperEndpoint struct {
	Address         string
	HostPublicKey   string
	HostFingerprint string
}

type Helper struct {
	config        HelperConfig
	listener      net.Listener
	endpoint      HelperEndpoint
	done          chan struct{}
	once          sync.Once
	hostPrivate   ed25519.PrivateKey
	connections   map[net.Conn]struct{}
	connectionsMu sync.Mutex
	connectionsWG sync.WaitGroup
	closing       bool
}

func StartHelper(ctx context.Context, cfg HelperConfig) (*Helper, error) {
	if _, err := uuid.Parse(cfg.OperationID); err != nil {
		return nil, fmt.Errorf("operation_id must be UUID: %w", err)
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("deploy helper requires client_id")
	}
	if cfg.WorkerKey == nil {
		return nil, errors.New("deploy helper requires pinned worker public key")
	}
	if cfg.Identity == nil {
		return nil, errors.New("deploy helper requires identity generator")
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("deploy helper listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("deploy helper may listen only on an IP loopback address")
	}
	var hostPrivate ed25519.PrivateKey
	if cfg.HostSigner == nil {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		cfg.HostSigner, err = ssh.NewSignerFromKey(private)
		if err != nil {
			zero(private)
			return nil, err
		}
		hostPrivate = private
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, err
	}
	h := &Helper{
		config: cfg, listener: listener, done: make(chan struct{}), hostPrivate: hostPrivate,
		connections: make(map[net.Conn]struct{}),
		endpoint: HelperEndpoint{
			Address:         listener.Addr().String(),
			HostPublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cfg.HostSigner.PublicKey()))),
			HostFingerprint: ssh.FingerprintSHA256(cfg.HostSigner.PublicKey()),
		},
	}
	go h.serve(ctx)
	return h, nil
}

func (h *Helper) Endpoint() HelperEndpoint { return h.endpoint }
func (h *Helper) Done() <-chan struct{}    { return h.done }

func (h *Helper) Close() error {
	h.once.Do(func() {
		_ = h.listener.Close()
		h.connectionsMu.Lock()
		h.closing = true
		for connection := range h.connections {
			_ = connection.Close()
		}
		h.connectionsMu.Unlock()
		h.connectionsWG.Wait()
		zero(h.hostPrivate)
		close(h.done)
	})
	return nil
}

func (h *Helper) serve(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			h.Close()
		case <-h.done:
		}
	}()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			return
		}
		if !h.trackConnection(conn) {
			_ = conn.Close()
			return
		}
		go h.serveConn(conn)
	}
}

func (h *Helper) serveConn(raw net.Conn) {
	defer h.untrackConnection(raw)
	defer raw.Close()
	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != HelperUser || !keysEqual(key, h.config.WorkerKey) {
				return nil, errors.New("deploy helper authentication rejected")
			}
			return &ssh.Permissions{Extensions: map[string]string{"worker-fingerprint": ssh.FingerprintSHA256(key)}}, nil
		},
		MaxAuthTries: 1,
	}
	serverConfig.AddHostKey(h.config.HostSigner)
	_, channels, requests, err := ssh.NewServerConn(raw, serverConfig)
	if err != nil {
		return
	}
	h.startTracked(func() { rejectGlobalRequests(requests) })
	for channel := range channels {
		if channel.ChannelType() != "session" {
			_ = channel.Reject(ssh.Prohibited, "only deploy protocol sessions are allowed")
			continue
		}
		stream, reqs, err := channel.Accept()
		if err != nil {
			continue
		}
		if !h.startTracked(func() { h.serveSession(stream, reqs) }) {
			_ = stream.Close()
			return
		}
	}
}

func (h *Helper) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	executed := false
	for request := range requests {
		if request.Type != "exec" || executed {
			_ = request.Reply(false, nil)
			continue
		}
		command, ok := parseExecPayload(request.Payload)
		if !ok || command != HelperCommand {
			_ = request.Reply(false, nil)
			continue
		}
		executed = true
		_ = request.Reply(true, nil)
		status := uint32(0)
		if err := h.execute(channel); err != nil {
			status = 1
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

func (h *Helper) execute(stream io.ReadWriter) error {
	decoder := json.NewDecoder(io.LimitReader(stream, 64*1024))
	request, err := decodeHelperRequest(decoder, h.config.OperationID, h.config.ClientID)
	if err != nil {
		response := HelperResponse{Schema: HelperSchema, Status: "error", Error: err.Error()}
		_ = json.NewEncoder(stream).Encode(response)
		return err
	}
	identity, err := h.config.Identity.GenerateInstallationIdentity(request.OperationID, request.ClientID)
	response := HelperResponse{Schema: HelperSchema, OperationID: request.OperationID, RequestID: request.RequestID}
	if err != nil {
		response.Status, response.Error = "error", err.Error()
	} else {
		response.Status = "ok"
		response.Identity = &PublicIdentity{PublicKey: identity.PublicKey, Fingerprint: identity.Fingerprint}
	}
	if encodeErr := json.NewEncoder(stream).Encode(response); encodeErr != nil {
		return encodeErr
	}
	return err
}

func (h *Helper) trackConnection(connection net.Conn) bool {
	h.connectionsMu.Lock()
	defer h.connectionsMu.Unlock()
	if h.closing {
		return false
	}
	h.connections[connection] = struct{}{}
	h.connectionsWG.Add(1)
	return true
}

func (h *Helper) untrackConnection(connection net.Conn) {
	h.connectionsMu.Lock()
	delete(h.connections, connection)
	h.connectionsMu.Unlock()
	h.connectionsWG.Done()
}

// startTracked closes the Add/Wait race by serializing admission with Close's
// transition to closing. Done is not closed until every admitted SSH worker
// goroutine has returned.
func (h *Helper) startTracked(run func()) bool {
	h.connectionsMu.Lock()
	defer h.connectionsMu.Unlock()
	if h.closing {
		return false
	}
	h.connectionsWG.Add(1)
	go func() {
		defer h.connectionsWG.Done()
		run()
	}()
	return true
}

func rejectGlobalRequests(requests <-chan *ssh.Request) {
	for request := range requests {
		if request.WantReply {
			_ = request.Reply(false, nil)
		}
	}
}

func parseExecPayload(payload []byte) (string, bool) {
	if len(payload) < 4 {
		return "", false
	}
	size := binary.BigEndian.Uint32(payload[:4])
	if size != uint32(len(payload)-4) || size > 1024 {
		return "", false
	}
	return string(payload[4 : 4+size]), true
}

func keysEqual(a, b ssh.PublicKey) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Type() == b.Type() && string(a.Marshal()) == string(b.Marshal())
}
