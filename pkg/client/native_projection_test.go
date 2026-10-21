package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

type projectionAck struct {
	bytes.Buffer
	closed bool
}

func (w *projectionAck) Close() error { w.closed = true; return nil }

func TestProjectionNeverFallsBackToPlainSVN(t *testing.T) {
	c := New(Options{SvnPath: filepath.Join(t.TempDir(), "must-not-run.exe"), ProjectionRemove: func(context.Context, ProjectionRemoval) error { return nil }})
	if _, err := c.Cleanup(t.Context(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "plain SVN fallback is disabled") {
		t.Fatal("anchor fell back to plain SVN", err)
	}
}

func TestProjectionProtocolBindsRequestAndLeavesFinalReceipt(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	ack := &projectionAck{}
	calls := 0
	p := &projectionSplitter{ctx: t.Context(), root: root, token: "operation", inner: &output, ack: ack,
		remove: func(context.Context, ProjectionRemoval) error { calls++; return nil }}
	request := ProjectionRemoval{Schema: "filees.svn-projection/v1", Operation: "operation", Sequence: 1, Root: root, Path: "folder/remote.bin", FileID: "123:456"}
	raw, _ := json.Marshal(request)
	for _, b := range append(raw, '\n') {
		if _, err := p.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	final := `{"schema":"filees.native-svn/v1","ok":true}`
	if _, err := io.WriteString(p, final); err != nil {
		t.Fatal(err)
	}
	if err := p.flush(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || ack.String() != "projection-ok operation 1\n" || output.String() != final {
		t.Fatal(calls, ack.String(), output.String())
	}
}

func TestProjectionProtocolNeverAcknowledgesUnboundOrRefusedRequest(t *testing.T) {
	for _, scenario := range []string{"token", "sequence", "root", "path", "id", "unknown", "refusal", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			ack := &projectionAck{}
			var output bytes.Buffer
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			aborted := false
			p := &projectionSplitter{ctx: ctx, root: root, token: "operation", inner: &output, ack: ack,
				abort: func() { aborted = true },
				remove: func(context.Context, ProjectionRemoval) error {
					calls++
					if scenario == "refusal" {
						return errors.New("local data changed")
					}
					return nil
				}}
			r := ProjectionRemoval{Schema: "filees.svn-projection/v1", Operation: "operation", Sequence: 1, Root: root, Path: "file.bin", FileID: "123:456"}
			switch scenario {
			case "token":
				r.Operation = "foreign"
			case "sequence":
				r.Sequence = 2
			case "root":
				r.Root = t.TempDir()
			case "path":
				r.Path = "../outside"
			case "id":
				r.FileID = "123:00456"
			case "cancel":
				cancel()
			}
			raw, _ := json.Marshal(r)
			if scenario == "unknown" {
				raw = append(raw[:len(raw)-1], []byte(`,"authorize_anything":true}`)...)
			}
			if _, err := p.Write(append(raw, '\n')); err == nil || ack.Len() != 0 || !ack.closed || !aborted {
				t.Fatal("unbound removal acknowledged", err, ack.String())
			}
			if scenario != "refusal" && calls != 0 {
				t.Fatal("unbound callback ran")
			}
		})
	}
}
