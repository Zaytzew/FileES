// Package opjournal locates the daemon's operational log.
//
// FileES keeps two layers of record and they answer different questions. The
// per-repository journal at <repo>/.filees/logs/errors.jsonl holds current
// activity - commits, queues, sync - and is separated per repository because
// that is how the work is separated. This is the other layer: errors belonging
// to the daemon rather than to any one repository, local failures and
// communication failures alike.
//
// They share one file on purpose. A local fault and a transport fault
// routinely arrive together, and splitting them - by kind, by server, by
// channel - would scatter one incident across several files and leave nobody
// able to read the sequence. What tells them apart is the scope carried in
// each record, which errmap already writes, so the channel is identified in
// the content and never in the path.
//
// The gap this closes was measured on 2026-09-03. An activation stalled for
// twenty minutes and nothing recorded why: the per-repository journal could
// not hold it, because activation runs before any repository exists; the
// daemon's own logger writes to stderr, which in a desktop install goes
// nowhere; and the interface, which knew within a second that something was
// wrong, wrote to nothing at all.
package opjournal

import (
	"os"
	"path/filepath"

	"filees/pkg/errmap"
)

// Path returns the operational log's location, creating its directory.
//
// It sits beside the daemon socket, in the directory that already holds the
// daemon's own state, so a log about the daemon lives where the daemon lives
// rather than under a server or repository that may not exist yet.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.jsonl"), nil
}

func Dir() (string, error) {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(runtimeDir) {
		return ensure(filepath.Join(runtimeDir, "filees-logs"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return ensure(filepath.Join(home, ".filees", "logs"))
}

func ensure(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Open returns a sink appending to the operational log under scope.
//
// The daemon and the interface both write here, so the file is opened per
// write in append mode rather than held open: two processes appending whole
// short lines is the one concurrent pattern O_APPEND makes safe, and a handle
// held by the interface would stop the daemon replacing or rotating the file.
func Open(scope string) (*errmap.Sink, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return errmap.NewSink(appendWriter{path: path}, scope), nil
}

type appendWriter struct{ path string }

func (w appendWriter) Write(p []byte) (int, error) {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	return file.Write(p)
}
