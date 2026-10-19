package localrepo

import "errors"

// Control persists direction only, never runs effects on an IPC goroutine.
// runMu closes the race between a stale displayed button and a live executor.
func (j *ConversionJournal) Control(expected ConversionRecord, action string) (ConversionRecord, error) {
	if !j.runMu.TryLock() {
		return j.Get(), ErrConversionExecuting
	}
	defer j.runMu.Unlock()
	if j.Get() != expected {
		return j.Get(), errors.New("conversion control receipt changed")
	}
	switch action {
	case "retry":
		if expected.Blocked == "" {
			return expected, nil
		}
		return j.Retry(expected)
	case "rollback":
		if expected.Phase == ConversionWaiting {
			return j.Cancel(expected)
		}
		if expected.Phase == ConversionRollingBack && expected.Blocked != "" {
			return j.Retry(expected)
		}
		return j.BeginRollback(expected)
	}
	return expected, errors.New("unknown conversion control")
}
