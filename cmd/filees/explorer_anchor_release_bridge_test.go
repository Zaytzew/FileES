//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestAnchorReleaseBridgeRequiresExactReplyType(t *testing.T) {
	for _, restore := range []bool{false, true} {
		bridge := &anchorBridge{}
		helper, _ := runAnchorBridge(t, bridge)
		_, _ = io.WriteString(helper.toDaemon, "{\"ok\":true}\n")
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			if restore {
				done <- bridge.restoreRelease(ctx, "drawing.dwg", 0)
			} else {
				done <- bridge.prepareRelease(ctx, "drawing.dwg", "123", "456:789")
			}
		}()
		fields := helper.answer(t)
		prefix, command, suffix := "release-prepared", "prepare-release-bound", "123\t456:789"
		if restore {
			prefix, command, suffix = "release-restored", "restore-release", "0"
		}
		if fields[0] != command || fields[2] != "drawing.dwg" || strings.Join(fields[3:], "\t") != suffix {
			t.Fatalf("bad command: %q", fields)
		}
		_, _ = io.WriteString(helper.toDaemon, "state-set\t"+fields[1]+"\t"+hresultOK+"\n")
		select {
		case err := <-done:
			t.Fatalf("wrong reply authorized release: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		_, _ = io.WriteString(helper.toDaemon, prefix+"\t"+fields[1]+"\t"+hresultOK+"\n")
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("exact reply was not delivered")
		}
		cancel()
		bridge.stateMu.Lock()
		left := len(bridge.stateWait)
		bridge.stateMu.Unlock()
		if left != 0 {
			t.Fatal("release waiter retained")
		}
	}
}

func TestAnchorReleaseBridgeCancelReapsBeforeReturn(t *testing.T) {
	bridge := &anchorBridge{}
	helper, _ := runAnchorBridge(t, bridge)
	_, _ = io.WriteString(helper.toDaemon, "{\"ok\":true}\n")
	ctx, cancel := context.WithCancel(t.Context())
	started, reaped := make(chan struct{}), make(chan struct{})
	bridge.stopStateWriter = func() { close(started); <-reaped }
	done := make(chan error, 1)
	go func() { done <- bridge.prepareRelease(ctx, "file", "123", "456:789") }()
	helper.answer(t)
	cancel()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider not stopped")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before reap: %v", err)
	default:
	}
	close(reaped)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAnchorReleaseBridgeRefusesMalformedRequestBeforeQueue(t *testing.T) {
	bridge := &anchorBridge{}
	for _, binding := range []string{"", "1:0", "1:2\nrestore-release", "1:2:3", "-1:2"} {
		if bridge.prepareRelease(t.Context(), "file", "123", binding) == nil {
			t.Fatal("bad source identity accepted")
		}
	}
	if bridge.restoreRelease(t.Context(), ".filees/private", 1) == nil || bridge.restoreRelease(t.Context(), "file", -1) == nil {
		t.Fatal("bad restore accepted")
	}
	if err := bridge.restoreRelease(t.Context(), "file", 0); !errors.Is(err, errAnchorHelperGone) {
		t.Fatalf("unconnected provider: %v", err)
	}
}

func TestAnchorConnectedReceiptStartsRecoveryWithoutBlockingReader(t *testing.T) {
	started, stop := make(chan struct{}), make(chan struct{})
	bridge := &anchorBridge{onConnected: func(context.Context) { close(started); <-stop }}
	helper, _ := runAnchorBridge(t, bridge)
	if _, err := io.WriteString(helper.toDaemon, "{\"ok\":true,\"detail\":\"not connected\"}\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
		t.Fatal("wrong receipt started recovery")
	default:
	}
	if _, err := io.WriteString(helper.toDaemon, "{\"ok\":true,\"detail\":\"connected\"}\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("connected receipt ignored")
	}
	// The callback is still running. ACK reading must continue independently.
	done := make(chan error, 1)
	go func() { done <- bridge.restoreRelease(t.Context(), "file", 0) }()
	fields := helper.answer(t)
	if _, err := io.WriteString(helper.toDaemon, "release-restored\t"+fields[1]+"\t"+hresultOK+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery blocked response reader")
	}
	close(stop)
}
