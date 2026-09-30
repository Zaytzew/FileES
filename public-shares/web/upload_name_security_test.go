//go:build !windows

package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"testing"
)

func TestUploadValidatesDecodedOriginalName(t *testing.T) {
	for _, name := range []string{"\nreport.pdf", "report.pdf\r", "report\u0085.pdf", "report\u202egpj.exe", "hidden\u200f/report.pdf", "directory/report.pdf", "..\\report.pdf", "Opinia Łódź.pdf"} {
		t.Run(name, func(t *testing.T) {
			handler, store := uploadHandler(t, validUploadProjection("invite-token"))
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", "form-data; name=\"file\"; filename*=UTF-8''"+url.PathEscape(name))
			part, err := writer.CreatePart(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(part, "payload"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/atmprojekt/oferta-a?invite=invite-token", &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if name == "Opinia Łódź.pdf" {
				records, err := store.ListReady()
				if response.Code != http.StatusAccepted || err != nil || len(records) != 1 || records[0].OriginalName != name {
					t.Fatalf("valid upload: status=%d, records=%v, err=%v", response.Code, records, err)
				}
				return
			}
			if response.Code != http.StatusNotFound {
				t.Fatalf("invalid name accepted: status=%d", response.Code)
			}
			entries, err := os.ReadDir(store.Root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid name wrote state: %v, %v", entries, err)
			}
		})
	}
}
