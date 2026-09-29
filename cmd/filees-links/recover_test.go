package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/fcgi"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"filees/public-shares/storage"
)

type sensitivePanic struct{}

func (sensitivePanic) String() string { panic("must not invoke panic String") }

func TestRecoverPanicsNeutralResponseAndPrivateReport(t *testing.T) {
	for _, value := range []any{"secret-password", sensitivePanic{}, []string{"secret-token"}, nil, http.ErrAbortHandler} {
		var reports []string
		handler := recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/?v=secret-visit")
			w.Header().Set("Set-Cookie", "secret-cookie")
			w.Header().Set("Content-Length", "5000")
			w.Header().Set("Content-Encoding", "gzip")
			panic(value)
		}), func(message string) { reports = append(reports, message) })
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/secret-path?invite=secret-invite", strings.NewReader("secret-body"))
		handler.ServeHTTP(w, r)
		if w.Code != 500 || w.Body.String() != "internal error\n" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response %d %q %v", w.Code, w.Body.String(), w.Header())
		}
		for _, key := range []string{"Location", "Set-Cookie", "Content-Length", "Content-Encoding"} {
			if w.Header().Get(key) != "" {
				t.Fatalf("stale response header %s", key)
			}
		}
		if value == http.ErrAbortHandler {
			if len(reports) != 0 {
				t.Fatal("intentional abort reported as panic")
			}
		} else if len(reports) != 1 || !strings.Contains(reports[0], "TestRecoverPanics") || strings.Contains(reports[0], "secret") {
			t.Fatalf("unsafe or missing diagnostic: %q", reports)
		}
	}
}

func TestRecoverPanicsPreservesStartedStream(t *testing.T) {
	for _, mode := range []string{"write", "header", "flush", "abort"} {
		w := httptest.NewRecorder()
		handler := recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			switch mode {
			case "header":
				w.WriteHeader(http.StatusPartialContent)
			case "flush":
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Fatal(err)
				}
			default:
				_, _ = io.WriteString(w, "file-prefix")
			}
			if mode == "abort" {
				panic(http.ErrAbortHandler)
			}
			panic("private")
		}), func(string) {})
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if strings.Contains(w.Body.String(), "error") || w.Header().Get("Content-Length") != "100" {
			t.Fatalf("%s: download corrupted by recovery", mode)
		}
		if mode == "header" && w.Code != http.StatusPartialContent {
			t.Fatal("committed status changed")
		}
		if mode == "flush" && !w.Flushed {
			t.Fatal("stream flush lost")
		}
	}
}

func TestFastCGIServesNextRequestAfterPanic(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var active storage.Activity
	reports := make(chan string, 4)
	handler := recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !active.Enter() {
			t.Error("activity unexpectedly stopped")
			return
		}
		defer active.Leave()
		switch r.URL.Path {
		case "/panic":
			panic("secret-panic-value")
		case "/partial":
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "file-prefix")
			panic("secret-partial-value")
		case "/abort":
			panic(http.ErrAbortHandler)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}), func(message string) { reports <- message })
	done := make(chan error, 1)
	go func() { done <- fcgi.Serve(listener, handler) }()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	for _, path := range []string{"/panic", "/partial", "/abort"} {
		response := fastCGIRequest(t, listener.Addr().String(), path)
		if path == "/partial" {
			if !strings.Contains(response, "Status: 200") || !strings.Contains(response, "Content-Length: 100") || !strings.HasSuffix(response, "file-prefix") || strings.Contains(response, "internal error") {
				t.Fatalf("partial response %q", response)
			}
		} else if !strings.Contains(response, "Status: 500") {
			t.Fatalf("%s: %q", path, response)
		}
		if healthy := fastCGIRequest(t, listener.Addr().String(), "/healthy"); !strings.Contains(healthy, "Status: 204") {
			t.Fatalf("next request failed: %q", healthy)
		}
	}
	if len(reports) != 2 {
		t.Fatalf("panic reports = %d", len(reports))
	}
	for len(reports) > 0 {
		if strings.Contains(<-reports, "secret") {
			t.Fatal("request secrets in diagnostic")
		}
	}
	idle := make(chan struct{})
	go func() { active.StopAndWait(); close(idle) }()
	select {
	case <-idle:
	case <-time.After(time.Second):
		t.Fatal("panic leaked an active request")
	}
}

// A tiny real FastCGI responder exchange: BeginRequest, Params, Stdin, then
// read Stdout through EndRequest. No dependency on a third-party HTTP server.
func fastCGIRequest(t *testing.T, address, path string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var request bytes.Buffer
	record := func(kind byte, data []byte) {
		header := []byte{1, kind, 0, 1, 0, 0, 0, 0}
		binary.BigEndian.PutUint16(header[4:6], uint16(len(data)))
		request.Write(header)
		request.Write(data)
	}
	record(1, []byte{0, 1, 0, 0, 0, 0, 0, 0})
	var params bytes.Buffer
	for key, value := range map[string]string{"REQUEST_METHOD": "GET", "REQUEST_URI": path + "?invite=secret", "QUERY_STRING": "invite=secret", "SERVER_PROTOCOL": "HTTP/1.1", "SERVER_NAME": "localhost", "SERVER_PORT": "80", "SCRIPT_NAME": path, "REMOTE_ADDR": "127.0.0.1"} {
		params.WriteByte(byte(len(key)))
		params.WriteByte(byte(len(value)))
		params.WriteString(key)
		params.WriteString(value)
	}
	record(4, params.Bytes())
	record(4, nil)
	record(5, nil)
	if _, err := io.Copy(conn, &request); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	for {
		var header [8]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			t.Fatal(err)
		}
		length := int(binary.BigEndian.Uint16(header[4:6]))
		data := make([]byte, length+int(header[6]))
		if _, err := io.ReadFull(conn, data); err != nil {
			t.Fatal(err)
		}
		if header[1] == 6 {
			stdout.Write(data[:length])
		}
		if header[1] == 3 {
			return stdout.String()
		}
	}
}
