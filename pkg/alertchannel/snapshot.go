// Package alertchannel implements server-owned incident snapshots and a local,
// read-only consumer inbox. Reading an alert never acknowledges it to the server.
package alertchannel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const Schema = "filees.alert-channel/v1"
const MaxBytes = 256 << 10
const MaxRecords = 128

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

type Incident struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Version   int64     `json:"version"`
	Severity  string    `json:"severity"`
	Status    string    `json:"status"`
	Text      string    `json:"text"`
	FirstSeen time.Time `json:"first_seen"`
	ChangedAt time.Time `json:"changed_at"`
}
type Snapshot struct {
	Schema     string     `json:"schema"`
	RealmID    string     `json:"realm_id"`
	Epoch      string     `json:"epoch"`
	Generation int64      `json:"generation"`
	Incidents  []Incident `json:"incidents"`
}

func validText(s string) bool {
	if s == "" || len(s) > 4096 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (s Snapshot) Validate(realm string) error {
	if s.Schema != Schema || s.RealmID != realm || s.Generation < 1 || len(s.Incidents) > MaxRecords {
		return errors.New("invalid alert snapshot")
	}
	if _, err := uuid.Parse(realm); err != nil {
		return err
	}
	if _, err := uuid.Parse(s.Epoch); err != nil {
		return err
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	for _, v := range s.Incidents {
		if _, err := uuid.Parse(v.ID); err != nil {
			return err
		}
		if ids[v.ID] || keys[v.Key] || !keyPattern.MatchString(v.Key) || v.Version < 1 || !validText(v.Text) || v.FirstSeen.IsZero() || v.ChangedAt.Before(v.FirstSeen) {
			return errors.New("invalid alert incident")
		}
		if v.Status != "active" && v.Status != "resolved" {
			return errors.New("invalid alert status")
		}
		if v.Severity != "info" && v.Severity != "warning" && v.Severity != "error" {
			return errors.New("invalid alert severity")
		}
		ids[v.ID], keys[v.Key] = true, true
	}
	return nil
}
func Decode(data []byte, realm string) (Snapshot, error) {
	var s Snapshot
	if len(data) > MaxBytes {
		return s, errors.New("alert snapshot too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return s, errors.New("trailing alert data")
	}
	return s, s.Validate(realm)
}

// Change preserves an active incident's identity. Repeated identical publications
// are no-ops; a recurrence after resolution starts a new incident.
func (s Snapshot) Change(realm, key, severity, status, text string, now time.Time) (Snapshot, bool, error) {
	if !keyPattern.MatchString(key) || !validText(text) {
		return s, false, errors.New("invalid alert key or text")
	}
	if s.Schema == "" {
		s = Snapshot{Schema: Schema, RealmID: realm, Epoch: uuid.NewString(), Generation: 1, Incidents: []Incident{}}
	}
	if err := s.Validate(realm); err != nil {
		return s, false, err
	}
	s.Incidents = append([]Incident(nil), s.Incidents...)
	n := -1
	for i, v := range s.Incidents {
		if v.Key == key {
			n = i
			break
		}
	}
	if n < 0 && status == "resolved" {
		return s, false, errors.New("cannot resolve unknown incident")
	}
	if n >= 0 {
		old := s.Incidents[n]
		if old.Text == text && old.Status == status && old.Severity == severity {
			return s, false, nil
		}
		if now.Before(old.ChangedAt) {
			return s, false, errors.New("alert clock moved backwards")
		}
		if old.Status == "active" || status == "resolved" {
			old.Version++
			old.Text = text
			old.Status = status
			old.Severity = severity
			old.ChangedAt = now.UTC()
			s.Incidents[n] = old
		} else {
			s.Incidents[n] = newIncident(key, severity, status, text, now)
		}
	} else {
		// Prune only resolved records, oldest first by change time. Active alerts
		// never disappear just because the channel reached its size limit.
		if len(s.Incidents) >= MaxRecords {
			oldest := -1
			for i, v := range s.Incidents {
				if v.Status == "resolved" && (oldest < 0 || v.ChangedAt.Before(s.Incidents[oldest].ChangedAt)) {
					oldest = i
				}
			}
			if oldest < 0 {
				return s, false, errors.New("alert channel full of active incidents")
			}
			s.Incidents = append(s.Incidents[:oldest], s.Incidents[oldest+1:]...)
		}
		s.Incidents = append(s.Incidents, newIncident(key, severity, status, text, now))
	}
	s.Generation++
	if err := s.Validate(realm); err != nil {
		return s, false, err
	}
	raw, _ := json.Marshal(s)
	if len(raw) > MaxBytes {
		return s, false, fmt.Errorf("alert snapshot exceeds %d bytes", MaxBytes)
	}
	return s, true, nil
}
func newIncident(key, severity, status, text string, now time.Time) Incident {
	return Incident{ID: uuid.NewString(), Key: key, Version: 1, Severity: severity, Status: status, Text: text, FirstSeen: now.UTC(), ChangedAt: now.UTC()}
}
