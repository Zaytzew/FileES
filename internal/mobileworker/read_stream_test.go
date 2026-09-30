package mobileworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"runtime"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

type generatedReader struct {
	Reader
	fail bool
}

func (generatedReader) Youngest(context.Context, string) (int64, error) { return 1, nil }
func (generatedReader) FileSize(context.Context, string, string, int64) (int64, error) {
	return 64 << 20, nil
}
func (r generatedReader) Cat(_ context.Context, _, _ string, _ int64, dst io.Writer) (int64, string, error) {
	h := sha256.New()
	block := bytes.Repeat([]byte("a"), 32<<10)
	var size int64
	for i := 0; i < 2048; i++ {
		n, err := io.MultiWriter(dst, h).Write(block)
		size += int64(n)
		if err != nil {
			return 0, "", err
		}
		if r.fail {
			return 0, "", errors.New("reader interrupted")
		}
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

type rejectedReadOutput struct{}

func (rejectedReadOutput) Write([]byte) (int, error) { return 0, errors.New("receiver disconnected") }

func TestDispatchReadDiskSpoolBoundedAndCleaned(t *testing.T) {
	for _, mode := range []string{"large", "read-failure", "write-failure", "denied"} {
		t.Run(mode, func(t *testing.T) {
			d := newDispatcher(t, "unused", "r")
			d.Appender.SpoolDir = t.TempDir()
			d.Browser.Reader = generatedReader{fail: mode == "read-failure"}
			if mode == "denied" {
				d.Browser.Authority = fakeAuthority{access: ""}
			}
			frame := frameRequest(t, uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: "r", Path: "large.bin"}, nil)
			var out io.Writer = io.Discard
			if mode == "write-failure" {
				out = rejectedReadOutput{}
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := d.Serve(context.Background(), bytes.NewReader(frame), out)
			runtime.ReadMemStats(&after)
			if (err != nil) != (mode == "write-failure") {
				t.Fatalf("Serve: %v", err)
			}
			if after.TotalAlloc-before.TotalAlloc > 8<<20 {
				t.Fatalf("buffered read: allocated %d", after.TotalAlloc-before.TotalAlloc)
			}
			assertReadSpoolIdle(t, d.Appender.SpoolDir)
		})
	}
}
