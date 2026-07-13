package ipcserver_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

func TestServerShutdownClosesEventStreams(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	server := ipcserver.New(socket)
	serverCtx, stopServer := context.WithCancel(context.Background())
	if err := server.Start(serverCtx); err != nil {
		t.Fatal(err)
	}

	clientCtx, stopClient := context.WithCancel(context.Background())
	defer stopClient()
	events, err := ipcclient.New(socket, "server-lifecycle-test").Subscribe(clientCtx)
	if err != nil {
		t.Fatal(err)
	}
	stopServer()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("unexpected event after server shutdown")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event stream remained open after server shutdown")
	}
}
