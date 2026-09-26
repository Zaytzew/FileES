package mobileworker

import (
	"encoding/json"
	"errors"
	"filees/internal/fsdurable"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "filees/pkg/mobile/v1"
)

// Ledger is the durable, normal source of operation state. Revprop on the
// committed revision is only its recovery anchor: if the worker dies between the
// commit and the COMMITTED write, the revision's filees:request-id proves the
// commit landed and the ledger is rebuilt from it.
type Ledger struct {
	Dir string
}

// Record is one append operation's durable state.
type Record struct {
	RequestID   string     `json:"request_id"`
	ClientID    string     `json:"client_id"`
	RepoID      string     `json:"repo_id"`
	Path        string     `json:"path"`
	PayloadHash string     `json:"payload_hash"`
	State       v1.OpState `json:"state"`
	Revision    int64      `json:"revision,omitempty"`
	FinalPath   string     `json:"final_path,omitempty"`
	UpdatedAt   string     `json:"updated_at"`
}

func (l Ledger) path(requestID string) string {
	return filepath.Join(l.Dir, requestID+".json")
}

// Lookup returns the record for requestID, or (nil, nil) if none exists.
func (l Ledger) Lookup(requestID string) (*Record, error) {
	raw, err := os.ReadFile(l.path(requestID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("decode ledger record: %w", err)
	}
	return &rec, nil
}

// Put atomically writes rec (temp file, fsync, rename, dir fsync), 0600.
func (l Ledger) Put(rec Record) error {
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(l.Dir, ".ledger-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, l.path(rec.RequestID)); err != nil {
		return err
	}
	return fsdurable.SyncDir(l.Dir)
}

// maxLogFieldLen bounds every field LogError writes, so one adversarial or
// pathological error string cannot grow the file unboundedly.
const maxLogFieldLen = 512

// LogError appends one bounded, best-effort diagnostic line to
// <Dir>/errors.log for a request the client only ever saw a generic, masked
// error for (Dispatcher.writeError's "worker.failed" fallback never leaks
// raw tool text to the phone - see internal/servertool/mobile_entry.go's
// forced-command boundary). This directory is the same one the ledger
// already writes durable records into under obsandbox's existing rwc
// unveil, so this needs no new sandbox surface. An administrator with a
// shell on the host can grep this file by request_id; the phone never sees
// it. A failure to write is swallowed: logging must never affect a response
// already being sent.
func (l Ledger) LogError(requestID, clientID, operation, cause string) {
	line := fmt.Sprintf(
		"%s\trequest_id=%s\tclient_id=%s\top=%s\tcause=%s\n",
		time.Now().UTC().Format(time.RFC3339Nano),
		sanitizeLogField(requestID), sanitizeLogField(clientID),
		sanitizeLogField(operation), sanitizeLogField(cause),
	)
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, "errors.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// sanitizeLogField folds control characters (the file is tab-delimited, one
// record per line) and truncates so one field cannot inject a fake record or
// grow the file unboundedly.
func sanitizeLogField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	if len(s) > maxLogFieldLen {
		s = s[:maxLogFieldLen]
	}
	return s
}
