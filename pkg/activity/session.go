package activity

import (
	"errors"
	"time"
)

const sessionLimit = 20

// SessionEvent marks daemon lifecycle, never repository or publication success.
// "stopping" and "restarting" mean requested, not successfully completed.
type SessionEvent struct {
	ID      string    `json:"id"`
	Phase   string    `json:"phase"`
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

func validSessionEvent(e SessionEvent) bool {
	return e.ID != "" && !e.At.IsZero() && (e.Phase == "started" || e.Phase == "stopping" || e.Phase == "restarting")
}

func (j *Journal) RecordSession(e SessionEvent) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if e.At.IsZero() {
		e.At = j.now().UTC()
	}
	if !validSessionEvent(e) {
		return errors.New("invalid session event")
	}
	for _, old := range j.sessions {
		if old.ID == e.ID && old.Phase == e.Phase {
			return nil
		}
	}
	before := j.sessions
	next := append(append([]SessionEvent(nil), before...), e)
	if len(next) > sessionLimit {
		next = next[len(next)-sessionLimit:]
	}
	j.sessions = next
	if err := j.persist(); err != nil {
		j.sessions = before
		return err
	}
	return nil
}

func (j *Journal) Sessions() []SessionEvent {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]SessionEvent(nil), j.sessions...)
}
