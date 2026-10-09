//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func renameTestFrame(t *testing.T, phase, token, flags string) string {
	t.Helper()
	id, err := anchorRemoteIdentity("source", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{phase, token, "1", "10", "11", "12", flags, id, `\point\source`, "42", `\point\target`}, "\t")
}

func TestAnchorRenameFrameStrictTypedScope(t *testing.T) {
	good := renameTestFrame(t, "rename-request", "1", "7")
	if _, _, ok := parseAnchorRenameFrame(good); !ok {
		t.Fatal("valid refused")
	}
	for _, bad := range []string{
		strings.Replace(good, "rename-request", "delete-request", 1),
		strings.Replace(good, "\t7\t", "\t6\t", 1),
		strings.Replace(good, "\t7\t", "\t3\t", 1),
		strings.Replace(good, "\t1\t1\t", "\t01\t1\t", 1),
		good + "\tmore", good + "\r", strings.Replace(good, "\t11\t", "\t10\t", 1),
		strings.Replace(good, `\point\target`, `\point\source`, 1),
	} {
		if _, _, ok := parseAnchorRenameFrame(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestAnchorRenameRequiresAckAndExactCompletion(t *testing.T) {
	for _, first := range []string{"ack", "completion"} {
		t.Run(first, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			out, done := make(chan string, 8), make(chan error, 1)
			r := newAnchorRenameTransport(ctx, func(ctx context.Context, cb anchorRenameCallback, physical func(context.Context) error) error {
				err := physical(ctx)
				done <- err
				return err
			}, func(f func()) { go f() }, func(s string) { out <- s })
			r.receive(renameTestFrame(t, "rename-request", "1", "7"))
			select {
			case s := <-out:
				if s != "rename-allow\t1\t1" {
					t.Fatal(s)
				}
			case <-time.After(time.Second):
				t.Fatal("no grant")
			}
			completion := renameTestFrame(t, "rename-complete", "0", "0")
			r.receive(strings.Replace(completion, `\point\target`, `\point\other`, 1))
			r.receive("delete-acked\t1\t" + hresultOK)
			if first == "ack" {
				r.receive("rename-acked\t1\t" + hresultOK)
			} else {
				r.receive(completion)
			}
			select {
			case <-done:
				t.Fatal("one evidence completed move")
			case <-time.After(20 * time.Millisecond):
			}
			if first == "ack" {
				r.receive(completion)
			} else {
				r.receive("rename-acked\t1\t" + hresultOK)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("not completed")
			}
		})
	}
}

func TestAnchorRenameCanceledAndPendingStayFenced(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	out, done := make(chan string, 8), make(chan struct{})
	r := newAnchorRenameTransport(ctx, func(ctx context.Context, cb anchorRenameCallback, physical func(context.Context) error) error {
		defer close(done)
		if err := physical(ctx); err == nil {
			return errors.New("must not complete")
		} else {
			return err
		}
	}, func(f func()) { go f() }, func(s string) { out <- s })
	r.receive(renameTestFrame(t, "rename-request", "1", "7"))
	<-out
	cancel()
	<-done
	r.receive(renameTestFrame(t, "rename-request", "2", "7"))
	if s := <-out; s != "rename-deny\t2\t1" {
		t.Fatal(s)
	}
}
