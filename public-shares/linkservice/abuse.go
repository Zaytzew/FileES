package linkservice

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"
)

type AbuseConfig struct {
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	SignalSocket   string   `json:"signal_socket,omitempty"`
}

// ListenAbuse exposes no public route. An administrator must prepare a private
// 0700 directory owned by the links service; the socket itself is 0600. It never
// unlinks an existing socket (which could belong to a still running process).
func (r Runtime) ListenAbuse() (net.Listener, func(), error) {
	path := r.Config.Abuse.SignalSocket
	noop := func() {}
	if path == "" {
		return nil, noop, nil
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, noop, errors.New("abuse signal socket requires a private 0700 directory")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, noop, errors.New("abuse signal socket path already exists or is inaccessible")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, noop, errors.New("cannot create abuse signal socket")
	}
	listener.SetUnlinkOnClose(false)
	created, _ := os.Lstat(path)
	cleanup := func() {
		_ = listener.Close()
		if current, err := os.Lstat(path); err == nil && created != nil && os.SameFile(created, current) {
			_ = os.Remove(path)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		cleanup()
		return nil, noop, errors.New("cannot protect abuse signal socket")
	}
	return listener, cleanup, nil
}

// ServeAbuse uses a single writer and bounded write deadline. There is no input
// protocol, work queue or per-connection goroutine. The caller closes listener
// on shutdown; the only payload is an ephemeral snapshot of current bans.
func (r Runtime) ServeAbuse(ctx context.Context, listener net.Listener) {
	for ctx.Err() == nil {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		_ = json.NewEncoder(conn).Encode(r.Abuse.Snapshot())
		_ = conn.Close()
	}
}
