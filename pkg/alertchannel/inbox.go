package alertchannel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filees/internal/durable"
	contract "filees/pkg/contract/v1"
)

type inboxDisk struct {
	Snapshot Snapshot        `json:"snapshot"`
	Observed time.Time       `json:"observed"`
	Read     map[string]bool `json:"read"`
}
type Inbox struct {
	mu                  sync.Mutex
	path, server, realm string
	disk                inboxDisk
	failed              bool
}

func Open(path, server, realm string) (*Inbox, error) {
	b := &Inbox{path: path, server: server, realm: realm, failed: true, disk: inboxDisk{Read: map[string]bool{}}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxBytes*2+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes*2 {
		return b, errors.New("alert cache too large")
	}
	var saved inboxDisk
	if err = json.Unmarshal(raw, &saved); err != nil {
		return b, err
	}
	if err = saved.Snapshot.Validate(realm); err != nil {
		return b, err
	}
	b.disk = saved
	if b.disk.Read == nil {
		b.disk.Read = map[string]bool{}
	}
	return b, nil
}
func (b *Inbox) save(d inboxDisk) error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(b.path), ".alerts-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, b.path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(b.path))
}
func (b *Inbox) Accept(s Snapshot, now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.Validate(b.realm); err != nil {
		return err
	}
	if b.disk.Snapshot.Epoch == s.Epoch && s.Generation < b.disk.Snapshot.Generation {
		return errors.New("alert generation went backwards")
	}
	s.Incidents = append([]Incident(nil), s.Incidents...)
	next := inboxDisk{Snapshot: s, Observed: now.UTC(), Read: map[string]bool{}}
	for _, v := range s.Incidents {
		id := b.id(s, v)
		next.Read[id] = b.disk.Read[id] || v.Status == "resolved"
	}
	if err := b.save(next); err != nil {
		return err
	}
	b.disk = next
	b.failed = false
	return nil
}
func (b *Inbox) Failed() { b.mu.Lock(); defer b.mu.Unlock(); b.failed = true }
func (b *Inbox) id(s Snapshot, v Incident) string {
	return fmt.Sprintf("server-alert:%s:%s:%s:%d", b.server, s.Epoch, v.ID, v.Version)
}
func (b *Inbox) Notices() ([]contract.Notice, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]contract.Notice, 0, len(b.disk.Snapshot.Incidents))
	for _, v := range b.disk.Snapshot.Incidents {
		id := b.id(b.disk.Snapshot, v)
		out = append(out, contract.Notice{ID: id, ServerID: b.server, Source: "server", Status: v.Status, Severity: v.Severity, Stale: b.failed || time.Since(b.disk.Observed) > 5*time.Minute, ObservedAt: b.disk.Observed.Format(time.RFC3339Nano), Title: v.Text, CreatedAt: v.ChangedAt.Format(time.RFC3339Nano), Acked: b.disk.Read[id] || v.Status == "resolved"})
	}
	return out, nil
}
func (b *Inbox) Ack(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.disk.Read[id]; !ok {
		return nil
	}
	next := b.disk
	next.Read = make(map[string]bool, len(b.disk.Read))
	for k, v := range b.disk.Read {
		next.Read[k] = v
	}
	next.Read[id] = true
	if err := b.save(next); err != nil {
		return err
	}
	b.disk = next
	return nil
}
