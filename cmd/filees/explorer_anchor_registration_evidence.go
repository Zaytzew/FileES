//go:build !nocfapi

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"filees/pkg/localrepo"
	"filees/pkg/privatefile"
)

const registrationEvidenceLimit = 512 << 10
const registrationEvidenceRoots = 128

// A diagnostic sidecar outside all working copies. Nothing here authorizes an
// operation or repairs a registration; disk failures must not block CFAPI.
type registrationEvidence struct {
	mu         sync.Mutex
	directory  string
	sampled    map[string]bool
	frozen     map[string]bool
	retryAfter map[string]time.Time
	helperHash string
	warned     bool
}

type registrationEvidenceFile struct {
	Schema        string                         `json:"schema"`
	DaemonVersion string                         `json:"daemon_version"`
	HelperSHA256  string                         `json:"helper_sha256"`
	Lifecycle     *registrationEvidenceLifecycle `json:"lifecycle,omitempty"`
	LastHealthy   json.RawMessage                `json:"last_healthy,omitempty"`
	FirstAnomaly  json.RawMessage                `json:"first_anomaly,omitempty"`
	BeforeAnomaly json.RawMessage                `json:"before_anomaly,omitempty"`
	Recent        []json.RawMessage              `json:"recent"`
}

type registrationEvidenceLifecycle struct {
	Operation string `json:"operation"`
	Server    string `json:"server"`
	Repo      string `json:"repo"`
	State     string `json:"state"`
	Root      string `json:"root"`
	Pending   string `json:"pending,omitempty"`
}

func registrationEvidenceKey(root string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return hex.EncodeToString(sum[:]) + ".json"
}

// Only known lifecycle roots are observed. Precheck of an arbitrary directory
// (including another provider) never produces a persistent diagnostic dump.
func (m *anchorManager) registrationTrace(command *exec.Cmd, args []string) {
	if runtime.GOOS != "windows" || m.profiles == "" || m.lifecycle == nil || len(args) == 0 {
		return
	}
	switch args[0] {
	case "info", "register", "shell-register", "unregister", "registration-snapshot":
	default:
		return
	}
	var root string
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--root" {
			root = args[i+1]
			break
		}
	}
	var record *localrepo.Record
	for _, r := range m.lifecycle.List() {
		if r.Anchor && (strings.EqualFold(filepath.Clean(r.LocalPath), filepath.Clean(root)) ||
			r.PendingLocalPath != "" && strings.EqualFold(filepath.Clean(r.PendingLocalPath), filepath.Clean(root))) {
			copy := r
			record = &copy
			break
		}
	}
	if record == nil {
		return
	}
	m.registration.mu.Lock()
	defer m.registration.mu.Unlock()
	d := &m.registration
	if d.sampled == nil {
		d.sampled = make(map[string]bool)
		d.frozen = make(map[string]bool)
		d.retryAfter = make(map[string]time.Time)
		d.directory = filepath.Join(filepath.Dir(m.profiles), "cfapi-registration-evidence")
		// The helper executable is local, not a user document. Hash once per manager.
		if f, err := os.Open(m.helper); err == nil {
			h := sha256.New()
			_, err = io.Copy(h, io.LimitReader(f, 64<<20))
			f.Close()
			if err == nil {
				d.helperHash = hex.EncodeToString(h.Sum(nil))
			}
		}
	}
	key := registrationEvidenceKey(root)
	if !d.sampled[key] && len(d.sampled) >= registrationEvidenceRoots {
		return
	}
	mode := "mutation"
	if args[0] == "info" {
		mode = "anomaly"
		if !d.sampled[key] {
			mode = "sample"
		}
		// First bad evidence survives daemon restarts. Do not keep probing a known
		// failure until an explicit registration operation changes it.
		if !d.sampled[key] {
			if prior, err := d.read(key); err == nil {
				d.frozen[key] = len(prior.FirstAnomaly) > 0
			}
		}
		if d.frozen[key] {
			d.sampled[key] = true
			return
		}
		if time.Now().Before(d.retryAfter[key]) {
			return
		}
	}
	d.sampled[key] = true
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return
	}
	// Remove inherited overrides. The helper receives only this invocation's ID.
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, "FILEES_REGISTRATION_TRACE") && !strings.EqualFold(name, "FILEES_REGISTRATION_OPERATION") {
			filtered = append(filtered, entry)
		}
	}
	command.Env = append(filtered, "FILEES_REGISTRATION_TRACE="+mode, "FILEES_REGISTRATION_OPERATION="+hex.EncodeToString(id[:]))
	lifecycle := &registrationEvidenceLifecycle{record.OperationID, record.ServerID, record.RepoID, string(record.State), record.LocalPath, record.PendingLocalPath}
	command.Stderr = &registrationEvidenceWriter{accept: func(raw []byte) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if err := d.save(key, raw, lifecycle); err != nil && !d.warned {
			d.warned = true
			m.log.Warnf("CFAPI registration evidence unavailable: %v", err)
		}
		var h struct {
			Anomaly bool   `json:"anomaly"`
			Healthy bool   `json:"healthy"`
			Phase   string `json:"phase"`
		}
		if json.Unmarshal(raw, &h) == nil {
			if h.Anomaly {
				d.frozen[key] = true
			}
			if h.Phase == "observation" && !h.Healthy {
				d.retryAfter[key] = time.Now().Add(time.Minute)
			}
		}
	}}
}

func (m *anchorManager) traceRelocation(ctx context.Context, record localrepo.Record, phase string) {
	if !record.Anchor || m.profiles == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	root := record.LocalPath
	if phase == "move_after" {
		root = record.PendingLocalPath
	}
	// Optional helper verb: old binaries reject it without altering the move.
	_, _ = m.call(ctx, "", "registration-snapshot", "--root", root, "--phase", phase)
}

// Drain even oversized/unrelated stderr without backpressure or unbounded RAM.
type registrationEvidenceWriter struct {
	line     []byte
	overflow bool
	accept   func([]byte)
}

func (w *registrationEvidenceWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			if !w.overflow && len(w.line) > 0 {
				w.accept(w.line)
			}
			w.line = nil
			w.overflow = false
			continue
		}
		if len(w.line) >= 32768 {
			w.overflow = true
			continue
		}
		w.line = append(w.line, b)
	}
	return len(p), nil
}

func (d *registrationEvidence) read(key string) (registrationEvidenceFile, error) {
	var result registrationEvidenceFile
	root, err := os.OpenRoot(d.directory)
	if err != nil {
		return result, err
	}
	defer root.Close()
	f, err := root.Open(key)
	if err != nil {
		return result, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, registrationEvidenceLimit+1))
	if err != nil {
		return result, err
	}
	if len(raw) > registrationEvidenceLimit {
		return result, errors.New("evidence exceeds limit")
	}
	err = json.Unmarshal(raw, &result)
	if err == nil && result.Schema != "filees.registration-journal/v1" {
		err = errors.New("unknown evidence schema")
	}
	return result, err
}

func (d *registrationEvidence) save(key string, raw []byte, lifecycle *registrationEvidenceLifecycle) error {
	var header struct {
		Schema  string `json:"schema"`
		Healthy bool   `json:"healthy"`
		Anomaly bool   `json:"anomaly"`
	}
	if len(raw) > 32768 || json.Unmarshal(raw, &header) != nil || header.Schema != "filees.registration-evidence/v1" {
		return nil
	}
	if st, err := os.Lstat(d.directory); err == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
		return errors.New("evidence directory is not a plain directory")
	}
	if err := privatefile.EnsureDir(d.directory); err != nil {
		return err
	}
	previous, err := d.read(key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} // preserve unreadable evidence
	if errors.Is(err, os.ErrNotExist) {
		dir, e := os.Open(d.directory)
		if e != nil {
			return e
		}
		entries, e := dir.ReadDir(registrationEvidenceRoots + 1)
		dir.Close()
		if e != nil && e != io.EOF {
			return e
		}
		if len(entries) >= registrationEvidenceRoots {
			return errors.New("evidence root limit reached")
		}
	}
	previous.Schema = "filees.registration-journal/v1"
	previous.DaemonVersion = version
	previous.HelperSHA256 = d.helperHash
	previous.Lifecycle = lifecycle
	// Attribution belongs to each snapshot, not only the mutable journal header:
	// an upgrade must not relabel the preserved first anomaly with a new hash.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	fields["daemon_version"], _ = json.Marshal(version)
	fields["helper_sha256"], _ = json.Marshal(d.helperHash)
	fields["lifecycle"], _ = json.Marshal(lifecycle)
	copied, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if header.Anomaly && len(previous.FirstAnomaly) == 0 {
		previous.FirstAnomaly = copied
		previous.BeforeAnomaly = bytes.Clone(previous.LastHealthy)
	}
	if header.Healthy {
		previous.LastHealthy = copied
	}
	previous.Recent = append(previous.Recent, copied)
	if len(previous.Recent) > 8 {
		previous.Recent = previous.Recent[len(previous.Recent)-8:]
	}
	encoded, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	if len(encoded) > registrationEvidenceLimit {
		return errors.New("evidence size limit reached")
	}
	root, err := os.OpenRoot(d.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	// One exclusive staging name per root also bounds crash residue. A leftover
	// is evidence, not permission to truncate it; report and preserve it.
	tmp := "." + key + ".pending"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(encoded)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(tmp, key)
}
