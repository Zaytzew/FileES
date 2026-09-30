package storagewatch

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
	"filees/pkg/alertchannel"
)

type Mail struct {
	ID         string `json:"id"`
	IncidentID string `json:"incident_id"`
	Text       string `json:"text"`
	Severity   string `json:"severity"`
}
type State struct {
	Observation *Observation          `json:"observation,omitempty"`
	Schema      string                `json:"schema"`
	Realm       string                `json:"realm"`
	Recipient   string                `json:"recipient"`
	Snapshot    alertchannel.Snapshot `json:"snapshot"`
	Pending     []Mail                `json:"pending,omitempty"`
}

const stateSchema = "filees.capacity-state/v1"

func Load(path, realm, email string) (State, error) {
	initial := State{Schema: stateSchema, Realm: realm, Recipient: email}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return initial, nil
	}
	if err != nil {
		return initial, err
	}
	defer f.Close()
	var s State
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&s); err != nil {
		return initial, err
	}
	if d.Decode(new(any)) != io.EOF {
		return initial, errors.New("trailing capacity state")
	}
	if s.Schema != stateSchema || s.Realm != realm || s.Recipient != email {
		return initial, errors.New("capacity state identity differs from configuration")
	}
	if s.Snapshot.Schema != "" {
		if err = s.Snapshot.Validate(realm); err != nil {
			return initial, err
		}
	}
	if len(s.Pending) > alertchannel.MaxRecords {
		return initial, errors.New("too many pending capacity emails")
	}
	if err := s.Observation.validate(); err != nil {
		return initial, err
	}
	return s, nil
}
func (s *State) Observe(v []Volume, p Policy, now time.Time) error {
	next, _, err := Apply(s.Snapshot, s.Realm, v, p, now)
	if err != nil {
		return err
	}
	observation, err := newObservation(v, next, now)
	if err != nil {
		return err
	}
	old := map[string]alertchannel.Incident{}
	for _, i := range s.Snapshot.Incidents {
		old[i.ID] = i
	}
	active := map[string]string{}
	for _, i := range next.Incidents {
		if i.Status == "active" {
			active[i.ID] = i.Severity
		}
	}
	pending := make([]Mail, 0, len(s.Pending))
	for _, m := range s.Pending {
		if active[m.IncidentID] == m.Severity {
			pending = append(pending, m)
		}
	}
	for _, i := range next.Incidents {
		before := old[i.ID]
		if i.Status != "active" || (before.Status == "active" && !(before.Severity == "warning" && i.Severity == "error")) {
			continue
		}
		// One outstanding message per incident; escalation replaces a queued warning.
		kept := pending[:0]
		for _, m := range pending {
			if m.IncidentID != i.ID {
				kept = append(kept, m)
			}
		}
		pending = kept
		pending = append(pending, Mail{ID: i.ID + "-" + i.Severity, IncidentID: i.ID, Text: i.Text, Severity: i.Severity})
	}
	s.Snapshot = next
	s.Observation = observation
	s.Pending = pending
	return nil
}
func (s State) Save(path string) error {
	if err := s.Observation.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("capacity state too large")
	}
	// A new successful measurement refreshes observation even without an alert.
	if previous, e := os.ReadFile(path); e == nil && string(previous) == string(raw) {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".capacity-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
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
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(path))
}

// Project copies only capacity incidents; unrelated operator notices survive.
func (s State) Project(remote alertchannel.Snapshot, now time.Time) (alertchannel.Snapshot, bool, error) {
	changed := false
	for _, i := range s.Snapshot.Incidents {
		found := false
		for _, r := range remote.Incidents {
			if r.Key == i.Key {
				found = true
				break
			}
		}
		if i.Status == "resolved" && !found {
			continue
		}
		next, did, err := remote.Change(s.Realm, i.Key, i.Severity, i.Status, i.Text, now)
		if err != nil {
			return remote, false, err
		}
		remote = next
		changed = changed || did
	}
	return remote, changed, nil
}
