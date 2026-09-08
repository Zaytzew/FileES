package web

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/public-shares/authority"
)

type sweepOnWrite struct {
	*httptest.ResponseRecorder
	once  sync.Once
	sweep func()
	fail  bool
}

func (w *sweepOnWrite) Write(p []byte) (int, error) {
	w.once.Do(w.sweep)
	if w.fail {
		return 0, errors.New("client disconnected")
	}
	return w.ResponseRecorder.Write(p)
}

func TestZIPKeepsEntireSetPinnedDuringExpiryAndReleasesOnDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "disconnect"}[disconnect], func(t *testing.T) {
			f := newWebFixture(t, nil)
			f.handler.Cache.Config.TTL = time.Second
			f.handler.MaxBundleFiles = 10
			f.handler.MaxBundleSize = 1 << 20
			size := int64(len("revision five payload"))
			f.source.trees[5] = append(f.source.trees[5], authority.TreeObject{RepoPath: "wydanie/second.pdf", DisplayName: "Second.pdf", Size: &size})
			visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))
			writer := &sweepOnWrite{ResponseRecorder: httptest.NewRecorder(), fail: disconnect, sweep: func() {
				*f.now = f.now.Add(2 * time.Second)
				result, err := f.handler.Cache.Sweep(context.Background(), *f.now)
				if err != nil || result.Files != 0 || result.Active != 2 {
					t.Fatalf("ZIP pins=%+v %v", result, err)
				}
			}}
			request := httptest.NewRequest(http.MethodPost, "https://example.test/atmprojekt/przetarg-2026/bundle?v="+url.QueryEscape(visit), strings.NewReader("all=1"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			f.handler.ServeHTTP(writer, request)
			if !disconnect {
				archive, err := zip.NewReader(bytes.NewReader(writer.Body.Bytes()), int64(writer.Body.Len()))
				if err != nil || len(archive.File) != 2 {
					t.Fatalf("ZIP=%v", err)
				}
				for _, entry := range archive.File {
					reader, err := entry.Open()
					if err != nil {
						t.Fatal(err)
					}
					raw, err := io.ReadAll(reader)
					reader.Close()
					if err != nil || string(raw) != "revision five payload" {
						t.Fatalf("ZIP bytes=%q %v", raw, err)
					}
				}
			}
			result, err := f.handler.Cache.Sweep(context.Background(), *f.now)
			if err != nil || result.Entries != 2 || result.Active != 0 {
				t.Fatalf("pins leaked: %+v %v", result, err)
			}
		})
	}
}

func TestBundlePreparationFailureReleasesEarlierPins(t *testing.T) {
	f := newWebFixture(t, nil)
	f.handler.Cache.Config.TTL = time.Second
	f.handler.MaxBundleFiles = 10
	f.handler.MaxBundleSize = int64(len("revision five payload"))
	size := int64(len("revision five payload"))
	f.source.trees[5] = append(f.source.trees[5], authority.TreeObject{RepoPath: "wydanie/second.pdf", DisplayName: "Second.pdf", Size: &size})
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))
	response := perform(f.handler, http.MethodPost, "https://example.test/atmprojekt/przetarg-2026/bundle?v="+url.QueryEscape(visit), "all=1", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if response.Code == 200 {
		t.Fatal("oversize bundle succeeded")
	}
	result, err := f.handler.Cache.Sweep(context.Background(), f.now.Add(2*time.Second))
	if err != nil || result.Entries != 2 || result.Active != 0 {
		t.Fatalf("partial preparation leaked pins: %+v %v", result, err)
	}
}
