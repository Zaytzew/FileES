//go:build windows

package backchannel

import (
	"context"
	"errors"
	"net"
)

// These are server-side service boundaries, not desktop IPC. Windows AF_UNIX
// support alone is not evidence of the required Unix account/group protection.
func ListenUnix(path, group string) (net.Listener, func(), error) {
	return nil, nil, errors.New("public-share service sockets require a Unix server")
}

func DialUnix(ctx context.Context, path string) (net.Conn, error) {
	return nil, errors.New("public-share service sockets require a Unix server")
}

func NewUnixDialer(path string) (func(context.Context) (net.Conn, error), error) {
	return nil, errors.New("public-share service sockets require a Unix server")
}
