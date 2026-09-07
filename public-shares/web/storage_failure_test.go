package web

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"filees/public-shares/authority"
	"filees/public-shares/storage"
)

type failingStorageBackend struct{ Backend }

func (b failingStorageBackend) Fetch(context.Context, authority.ObjectRequest) (authority.FetchedLeaf, error) {
	return authority.FetchedLeaf{}, fmt.Errorf("%w: /private/staging/path", storage.ErrUnavailable)
}
func TestStorageFailureIs503AfterAuthorizationAndZipRecovers(t *testing.T) {
	f := newWebFixture(t, nil)
	original := f.handler.Backend
	f.handler.Backend = failingStorageBackend{original}
	f.handler.MaxBundleFiles = 4096
	f.handler.MaxBundleSize = 10 << 30
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))
	base := "https://example.test/atmprojekt/przetarg-2026"
	q := "?v=" + url.QueryEscape(visit)
	if got := perform(f.handler, http.MethodGet, base+q, "", nil); got.Code != 200 {
		t.Fatal("listing failed")
	}
	for _, route := range []string{"/get/7f3a1c9e2b4d6a80", "/file/7f3a1c9e2b4d6a80", "/bundle"} {
		method, body := http.MethodGet, ""
		if route == "/bundle" {
			method, body = http.MethodPost, "all=1"
		}
		got := perform(f.handler, method, base+route+q, body, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if got.Code != 503 || strings.Contains(got.Body.String(), "/private") || got.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: %d %s", route, got.Code, got.Body.String())
		}
	}
	if got := perform(f.handler, http.MethodGet, base+"/get/7f3a1c9e2b4d6a80?v=invalid", "", nil); got.Code != 404 {
		t.Fatal("authorization exposed storage state")
	}
	f.handler.Backend = original
	got := perform(f.handler, http.MethodPost, base+"/bundle"+q, "all=1", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if got.Code != 200 {
		t.Fatalf("recovery: %d %s", got.Code, got.Body.String())
	}
	z, err := zip.NewReader(bytes.NewReader(got.Body.Bytes()), int64(got.Body.Len()))
	if err != nil || len(z.File) != 1 {
		t.Fatalf("invalid recovered ZIP: %v", err)
	}
}
