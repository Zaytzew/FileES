package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const displayLossGrace = 20 * time.Second
const displayRecoveryWindow = 15 * time.Minute

// Direct browser/history IPC calls share admission with controller gestures.
// Closing their window is not evidence that the call has already returned.
func (service *GUIService) beginDisplayCall() (func(), error) {
	service.actionAdmission.RLock()
	if service.recoveringDisplay {
		service.actionAdmission.RUnlock()
		return nil, errors.New(service.localizeText("display.recovering", "FileES is restoring its window."))
	}
	return service.actionAdmission.RUnlock, nil
}

func beginDirectDisplayCall(begin func() (func(), error)) (func(), error) {
	if begin == nil {
		return func() {}, nil
	} // Isolated service compositions.
	return begin()
}

// Browser loss is not a pixel/JavaScript health check. Only a previously seen
// browser disappearing continuously through successful observations arms it.
type displayHealth struct {
	seen         bool
	missingSince time.Time
}

func (health *displayHealth) lost(now time.Time, present bool, err error) bool {
	if err != nil || present {
		health.missingSince = time.Time{}
		if err == nil && present {
			health.seen = true
		}
		return false
	}
	if !health.seen {
		return false
	}
	if health.missingSince.IsZero() {
		health.missingSince = now
	}
	return now.Sub(health.missingSince) >= displayLossGrace
}

type displayRecoveryBudget struct {
	Started  time.Time `json:"started"`
	Attempts int       `json:"attempts"`
}

// A persistent, per-profile budget survives GUI-only self-restarts. Unknown
// state and backwards clock jumps fail closed instead of starting a loop.
func displayRecoveryAvailable(path string, now time.Time) (displayRecoveryBudget, error) {
	var budget displayRecoveryBudget
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 1024 || json.Unmarshal(data, &budget) != nil || budget.Started.IsZero() || budget.Attempts < 1 || budget.Attempts > 2 {
			return budget, fmt.Errorf("invalid recovery budget")
		}
		if now.Before(budget.Started) {
			return budget, fmt.Errorf("recovery clock moved backwards")
		}
	} else if !os.IsNotExist(err) {
		return budget, err
	}
	if budget.Started.IsZero() || now.Sub(budget.Started) >= displayRecoveryWindow {
		budget = displayRecoveryBudget{Started: now}
	}
	if budget.Attempts >= 2 {
		return budget, fmt.Errorf("automatic recovery limit reached")
	}
	return budget, nil
}

func takeDisplayRecovery(path string, now time.Time) error {
	budget, err := displayRecoveryAvailable(path, now)
	if err != nil {
		return err
	}
	budget.Attempts++
	data, err := json.Marshal(budget)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".display-recovery-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

// Transition-only log, at most two 1 MiB files. No IPC payload, repository
// path, browser command line or user data is included.
func recordDisplayHealth(dir, event, detail string) {
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	path := filepath.Join(dir, "gui-display.jsonl")
	if info, err := os.Stat(path); err == nil && info.Size() >= 1<<20 {
		if os.Rename(path, path+".previous") != nil {
			return
		}
	}
	entry, err := json.Marshal(struct {
		Time   time.Time `json:"time"`
		PID    int       `json:"pid"`
		Event  string    `json:"event"`
		Detail string    `json:"detail"`
	}{time.Now().UTC(), os.Getpid(), event, detail})
	if err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(entry, '\n'))
}
