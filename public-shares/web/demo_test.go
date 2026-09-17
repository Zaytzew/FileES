//go:build !windows

package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func assertDemoRefusal(t *testing.T, what string, recorder *httptest.ResponseRecorder) {
	t.Helper()
	body := recorder.Body.String()
	if recorder.Code != http.StatusNotFound || !strings.Contains(body, "To jest serwer demonstracyjny") || !strings.Contains(body, "This is a demo server") {
		t.Fatalf("%s: status=%d body=%s", what, recorder.Code, body)
	}
	if strings.Contains(body, "revision five payload") || strings.Contains(body, "atmprojekt") || recorder.Header().Get("Content-Disposition") != "" {
		t.Fatalf("%s: demo refusal leaked content or identity: headers=%v body=%s", what, recorder.Header(), body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "style-src 'sha256-") {
		t.Fatalf("%s: demo refusal headers=%v", what, recorder.Header())
	}
}

func TestDemoListsSharesButServesNoContent(t *testing.T) {
	f := newWebFixture(t, nil)
	f.handler.Demo = true
	visit := visitFromRedirect(t, perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026", "", nil))
	listing := perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026?v="+url.QueryEscape(visit), "", nil)
	if listing.Code != http.StatusOK || !strings.Contains(listing.Body.String(), "Projekt budowlany.pdf") {
		t.Fatalf("demo listing status=%d body=%s", listing.Code, listing.Body.String())
	}
	query := "?v=" + url.QueryEscape(visit)
	assertDemoRefusal(t, "get", perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026/get/7f3a1c9e2b4d6a80"+query, "", nil))
	assertDemoRefusal(t, "file", perform(f.handler, http.MethodGet, "https://example.test/atmprojekt/przetarg-2026/file/7f3a1c9e2b4d6a80"+query, "", map[string]string{"Range": "bytes=0-4"}))
	assertDemoRefusal(t, "head", perform(f.handler, http.MethodHead, "https://example.test/atmprojekt/przetarg-2026/file/7f3a1c9e2b4d6a80"+query, "", nil))
	assertDemoRefusal(t, "bundle", perform(f.handler, http.MethodPost, "https://example.test/atmprojekt/przetarg-2026/bundle"+query, "object=7f3a1c9e2b4d6a80", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}))
	assertDemoRefusal(t, "unknown channel", perform(f.handler, http.MethodGet, "https://example.test/nikt/nic/file/0000000000000000", "", nil))
	if entries, err := os.ReadDir(f.handler.Cache.Config.Root); err != nil || len(entries) != 0 {
		t.Fatalf("demo touched the leaf cache: entries=%v err=%v", entries, err)
	}
}

func TestDemoShowsUploadFormButAcceptsNoFile(t *testing.T) {
	token := "invite-token"
	handler, store := uploadHandler(t, validUploadProjection(token))
	handler.Demo = true
	form := httptest.NewRecorder()
	handler.ServeHTTP(form, httptest.NewRequest(http.MethodGet, "/atmprojekt/oferta-a?invite="+token, nil))
	if form.Code != http.StatusOK {
		t.Fatalf("demo upload form status=%d body=%s", form.Code, form.Body.String())
	}
	for _, target := range []string{"/atmprojekt/oferta-a?invite=" + token, "/nikt/nic"} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "material.pdf")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, "wniesiony-plik"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, target, &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assertDemoRefusal(t, "upload "+target, recorder)
	}
	if entries, err := os.ReadDir(store.Root); err != nil || len(entries) != 0 {
		t.Fatalf("demo accepted a file into quarantine: entries=%v err=%v", entries, err)
	}
}
