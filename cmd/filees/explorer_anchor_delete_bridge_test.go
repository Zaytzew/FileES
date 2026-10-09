//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnchorDeleteTransportRearmsOnlyRetiredOrUnpreparedEpoch(t *testing.T) {
	answers := make(chan string, 20)
	var wg sync.WaitGroup
	work := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	var prepared, completed atomic.Int32
	d := newAnchorDeleteTransport(t.Context(), func(context.Context, anchorDeleteCallback) error {
		if prepared.Add(1) == 1 {
			return errors.Join(errors.New("denied before any intent"), errAnchorDeleteNotPrepared)
		}
		return nil
	}, func(context.Context, anchorDeleteCallback) error { completed.Add(1); return nil }, 0, work, func(s string) { answers <- s })
	d.rearm = true
	first := d.session
	d.receive(deleteBridgeFrame("delete-request", "1"))
	<-answers
	wg.Wait()
	if first == d.session || d.token != "" {
		t.Fatal("explicit pre-intent refusal did not rearm")
	}
	d.receive(deleteBridgeFrame("delete-request", "2"))
	<-answers
	wg.Wait()
	second := d.session
	d.receive("delete-acked\t1\t" + hresultOK)
	d.receive(deleteBridgeFrame("delete-complete", "0"))
	wg.Wait()
	if completed.Load() != 0 {
		t.Fatal("old ACK completed a new epoch")
	}
	d.receive("delete-acked\t2\t" + hresultOK)
	wg.Wait()
	if completed.Load() != 1 || d.session == second || d.token != "" {
		t.Fatal("retired coordinator did not rearm")
	}
	d.receive("delete-acked\t2\t" + hresultOK)
	d.receive(deleteBridgeFrame("delete-complete", "0"))
	wg.Wait()
	if completed.Load() != 1 {
		t.Fatal("late completion replayed")
	}
}

func deleteBridgeFrame(kind, token string) string {
	request := "17"
	if kind == "delete-complete" {
		request = "0"
	}
	identity, _ := anchorRemoteIdentity("folder/plik 新.bin", 19, 4)
	return fmt.Sprintf("%s\t%s\t1\t2\t3\t%s\t0\t%s\t\\root\\folder\\plik 新.bin\t123", kind, token, request, identity)
}

func TestAnchorDeleteFrameStrictBinding(t *testing.T) {
	good := deleteBridgeFrame("delete-request", "7")
	_, cb, ok := parseAnchorDeleteFrame(good)
	if !ok || cb.Session != "" || cb.RequestKey != 17 || cb.FileID != 3 {
		t.Fatal(cb, ok)
	}
	f := strings.Split(good, "\t")
	for i, values := range map[int][]string{
		1: {"0", "07", "-7", "2147483648"}, 2: {"4294967296", "+1"}, 3: {"0", "02"},
		4: {"0", "2", "18446744073709551616"}, 5: {"0", "017", "+17"}, 6: {"1"},
		7: {"local", "remote-v1:19:4:../bad"}, 8: {"C:\\root", "\\root\r"}, 9: {"0", "0123"},
	} {
		for _, value := range values {
			bad := append([]string(nil), f...)
			bad[i] = value
			if _, _, ok := parseAnchorDeleteFrame(strings.Join(bad, "\t")); ok {
				t.Fatalf("accepted field %d=%q", i, value)
			}
		}
	}
	for _, bad := range []string{good + "\tmore", good + "\x00", good + "\n", good + string([]byte{255}), strings.Repeat("x", 9000)} {
		if _, _, ok := parseAnchorDeleteFrame(bad); ok {
			t.Fatalf("accepted malformed frame %q", bad[:10])
		}
	}
}

func TestAnchorDeleteTransportNeedsNativeACKAndExactCompletion(t *testing.T) {
	for _, first := range []string{"ack", "complete"} {
		t.Run(first, func(t *testing.T) {
			answers := make(chan string, 20)
			completed := make(chan anchorDeleteCallback, 2)
			var wg sync.WaitGroup
			work := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
			d := newAnchorDeleteTransport(t.Context(), func(_ context.Context, cb anchorDeleteCallback) error {
				if cb.Session == "" {
					t.Error("missing owned session")
				}
				return nil
			}, func(_ context.Context, cb anchorDeleteCallback) error { completed <- cb; return nil }, 0, work, func(s string) { answers <- s })
			d.receive(deleteBridgeFrame("delete-request", "7"))
			if answer := <-answers; answer != "delete-allow\t7\t1" {
				t.Fatal(answer)
			}
			ack, complete := "delete-acked\t7\t"+hresultOK, deleteBridgeFrame("delete-complete", "0")
			one, two := ack, complete
			if first == "complete" {
				one, two = two, one
			}
			d.receive(one)
			select {
			case <-completed:
				t.Fatal("permission/one phase became completion")
			default:
			}
			d.receive(two)
			select {
			case cb := <-completed:
				if cb.Session != d.session || cb.RequestKey != 0 {
					t.Fatal(cb)
				}
			case <-time.After(time.Second):
				t.Fatal("completion missing")
			}
			d.receive(two)
			d.receive(one)
			d.receive(deleteBridgeFrame("delete-request", "8"))
			if answer := <-answers; answer != "delete-deny\t8\t1" {
				t.Fatal(answer)
			}
			wg.Wait()
			select {
			case <-completed:
				t.Fatal("completion duplicated")
			default:
			}
		})
	}
}

func TestAnchorDeleteTransportRejectsForeignFailedAndCanceledCompletion(t *testing.T) {
	for _, scenario := range []string{"wrong-token", "failed-ack", "wrong-object", "flags", "cancel", "no-ack"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			answers := make(chan string, 2)
			var completed atomic.Int32
			var wg sync.WaitGroup
			work := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
			d := newAnchorDeleteTransport(ctx, func(context.Context, anchorDeleteCallback) error { return nil }, func(context.Context, anchorDeleteCallback) error { completed.Add(1); return nil }, 0, work, func(s string) { answers <- s })
			// An unsolicited completion cannot prime permission for a later pre.
			d.receive(deleteBridgeFrame("delete-complete", "0"))
			d.receive(deleteBridgeFrame("delete-request", "7"))
			<-answers
			ack, completion := "delete-acked\t7\t"+hresultOK, deleteBridgeFrame("delete-complete", "0")
			switch scenario {
			case "wrong-token":
				ack = "delete-acked\t8\t" + hresultOK
			case "failed-ack":
				ack = "delete-acked\t7\t0x80070005"
			case "wrong-object":
				f := strings.Split(completion, "\t")
				f[4] = "4"
				completion = strings.Join(f, "\t")
			case "flags":
				f := strings.Split(completion, "\t")
				f[6] = "1"
				completion = strings.Join(f, "\t")
			case "cancel":
				cancel()
			case "no-ack":
				ack = "ok\t7\tpath"
			}
			d.receive(ack)
			d.receive(completion)
			wg.Wait()
			if completed.Load() != 0 {
				t.Fatal("unproven deletion completed")
			}
		})
	}
}

func TestAnchorDeleteTransportTimeoutAndBoundedAdmission(t *testing.T) {
	answers := make(chan string, 40)
	started := make(chan struct{})
	var calls atomic.Int32
	var wg sync.WaitGroup
	work := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	d := newAnchorDeleteTransport(t.Context(), func(ctx context.Context, _ anchorDeleteCallback) error {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return nil // late success is NOT a grant
	}, func(context.Context, anchorDeleteCallback) error { t.Error("late completion"); return nil }, 50*time.Millisecond, work, func(s string) { answers <- s })
	d.receive(deleteBridgeFrame("delete-request", "1"))
	<-started
	for i := 2; i < 22; i++ {
		d.receive(deleteBridgeFrame("delete-request", fmt.Sprint(i)))
	}
	for range 21 {
		if answer := <-answers; !strings.HasPrefix(answer, "delete-deny\t") {
			t.Fatal(answer)
		}
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("unbounded preparation", calls.Load())
	}
	if d.session == newAnchorDeleteTransport(t.Context(), nil, nil, 0, work, func(string) {}).session {
		t.Fatal("reused provider session")
	}
}

func TestAnchorDeleteTransportDoesNotBlockFetchDraining(t *testing.T) {
	base := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	b := &anchorBridge{deletePrepare: func(ctx context.Context, _ anchorDeleteCallback) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, deleteComplete: func(context.Context, anchorDeleteCallback) error { return nil }, materialize: func(context.Context, string) (string, int64, error) { return filepath.Join(base, "bytes"), 1, nil }}
	h, _ := runAnchorBridge(t, b)
	io.WriteString(h.toDaemon, deleteBridgeFrame("delete-request", "7")+"\n")
	<-entered
	h.fetch(t, "fetch-1", "other.bin")
	if answer := h.answer(t); answer[0] != "ok" || answer[1] != "fetch-1" {
		t.Fatal("delete blocked drainer", answer)
	}
	close(release)
	if answer := h.answer(t); len(answer) != 3 || answer[0] != "delete-allow" {
		t.Fatal(answer)
	}
}

func TestAnchorDeleteProductBridgeDefaultsToRefusal(t *testing.T) {
	h, _ := runAnchorBridge(t, &anchorBridge{})
	io.WriteString(h.toDaemon, deleteBridgeFrame("delete-request", "7")+"\n")
	if answer := h.answer(t); len(answer) != 3 || answer[0] != "delete-deny" {
		t.Fatal(answer)
	}
}
