package mobileworker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

type readBudgetProbe struct {
	Reader
	t       *testing.T
	size    int64
	data    string
	reads   int
	sizeErr error
}

func (r *readBudgetProbe) Youngest(context.Context, string) (int64, error) { return 37, nil }
func (r *readBudgetProbe) FileSize(_ context.Context, _, _ string, rev int64) (int64, error) {
	if rev != 37 {
		r.t.Fatal("size queried at different revision")
	}
	return r.size, r.sizeErr
}
func (r *readBudgetProbe) Cat(_ context.Context, _, _ string, rev int64, dst io.Writer) (int64, string, error) {
	if rev != 37 {
		r.t.Fatal("content queried at different revision")
	}
	r.reads++
	n, err := io.WriteString(dst, r.data)
	if err != nil {
		return 0, "", errors.New("child replaced writer error")
	}
	return int64(n), sha([]byte(r.data)), nil
}

func TestReadBudgetAdmissionAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name               string
		limit, size        int64
		data, access, code string
		spaceErr, sizeErr  bool
		reads, probes      int
	}{
		{"exact", 4, 4, "data", "r", "", false, false, 1, 1},
		{"disabled", 0, 4, "data", "r", "", false, false, 1, 1},
		{"too large", 3, 4, "data", "r", "download.limit", false, false, 0, 0},
		{"negative size", 0, -1, "", "r", "download.limit", false, false, 0, 0},
		{"no space", 4, 4, "data", "r", "storage.unavailable", true, false, 0, 1},
		{"metadata error", 4, 4, "data", "r", "worker.failed", false, true, 0, 0},
		{"denied", 4, 4, "data", "", "access.denied", false, false, 0, 0},
		{"source overrun", 4, 4, "data!", "r", "download.limit", false, false, 1, 1},
		{"source short", 4, 4, "dat", "r", "worker.failed", false, false, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDispatcher(t, "unused", tc.access)
			d.Appender.SpoolDir = t.TempDir()
			r := &readBudgetProbe{t: t, size: tc.size, data: tc.data}
			if tc.sizeErr {
				r.sizeErr = errors.New("synthetic metadata failure")
			}
			d.Browser.Reader, d.Browser.MaxReadBytes = r, tc.limit
			probes := 0
			d.readSpace = func(path string, size int64) error {
				probes++
				if filepath.Clean(path) != d.Appender.SpoolDir || size != tc.size {
					t.Fatal("wrong volume or size")
				}
				if tc.spaceErr {
					return errors.New("synthetic unavailable capacity")
				}
				return nil
			}
			resp, payload := serve(t, d, frameRequest(t, uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: "r", Path: "file"}, nil))
			if tc.code == "" {
				if resp.Status != v1.StatusOK || string(payload) != tc.data {
					t.Fatalf("%+v %q", resp, payload)
				}
			} else if resp.Error == nil || resp.Error.Code != tc.code || len(payload) != 0 {
				t.Fatalf("%+v %q", resp, payload)
			}
			if r.reads != tc.reads || probes != tc.probes {
				t.Fatalf("reads=%d probes=%d", r.reads, probes)
			}
			if entries, err := os.ReadDir(d.Appender.SpoolDir); err != nil || len(entries) != 0 {
				t.Fatalf("leaked spool: %v %v", entries, err)
			}
		})
	}
}

func TestReadBoundWriterNeverWritesPastMetadata(t *testing.T) {
	var buf bytes.Buffer
	w := readBoundWriter{dst: &buf, remaining: 4}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("de")); n != 0 || !errors.Is(err, ErrReadLimit) {
		t.Fatalf("%d %v", n, err)
	}
	if n, err := w.Write([]byte("d")); n != 0 || !errors.Is(err, ErrReadLimit) {
		t.Fatalf("lost error: %d %v", n, err)
	}
	if buf.String() != "abc" {
		t.Fatal(buf.String())
	}
}
