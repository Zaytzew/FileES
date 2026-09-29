package main

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

// FastCGI does not recover handler panics. Keep recovery outside Activity so
// its deferred Leave (and the handler's other deferred cleanup) runs first.
// This protects this request goroutine, not background goroutines or fatal
// runtime failures. In particular, it is not a substitute for fixing a panic.
func recoverPanics(next http.Handler, report func(string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &trackedResponse{ResponseWriter: w}
		defer func() {
			failure := recover()
			if failure == nil {
				return
			}
			if failure != http.ErrAbortHandler {
				// Never stringify the panic value: even Error()/String() can
				// carry passwords, invitations or request bodies. Stack frames
				// contain symbols and line numbers only, not argument values.
				report(panicFrames(response.started))
			}
			if response.started {
				// Status/bytes already belong to a download. FastCGI exposes no
				// request-abort API: let it finish this truncated response, with
				// the original Content-Length, without appending an error page.
				return
			}
			// Remove a possibly prepared redirect, cookie, length or encoding.
			clear(w.Header())
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'")
			http.Error(w, "internal error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(response, r)
	})
}

type trackedResponse struct {
	http.ResponseWriter
	started bool
}

func (w *trackedResponse) WriteHeader(status int) {
	w.started = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *trackedResponse) Write(body []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(body)
}

// FastCGI's response supports Flush. Preserve streaming and ensure a flush
// commits the response for recovery purposes, including ResponseController.
func (w *trackedResponse) Flush() {
	w.started = true
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *trackedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func panicFrames(started bool) string {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var message strings.Builder
	fmt.Fprintf(&message, "filees-links: request panic; response_started=%t", started)
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&message, "\n%s:%d", frame.Function, frame.Line)
		if !more {
			break
		}
	}
	return message.String()
}
