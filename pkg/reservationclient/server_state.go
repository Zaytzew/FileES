package reservationclient

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
	contract "filees/pkg/contract/v1"
	"filees/pkg/privatefile"
	reservationv1 "filees/pkg/reservation/v1"
)

// ServerStatePath places the broker's local mirror beside, not inside, the
// authoritative SVN view. It never rewrites an invitation or client profile.
func ServerStatePath(viewCachePath string) string {
	if !filepath.IsAbs(viewCachePath) {
		return ""
	}
	return filepath.Join(filepath.Dir(viewCachePath), "server-state.json")
}

func LoadServerState(path, serverID string) (reservationv1.Result, bool, error) {
	r, ok, _, err := loadServerStateSnapshot(path, serverID)
	return r, ok, err
}

// Read bytes and receipt time from one inode across atomic replacements.
func loadServerStateSnapshot(path, serverID string) (reservationv1.Result, bool, time.Time, error) {
	if !filepath.IsAbs(path) {
		return reservationv1.Result{}, false, time.Time{}, errors.New("server state mirror path must be absolute")
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return reservationv1.Result{}, false, time.Time{}, nil
	}
	if err != nil {
		return reservationv1.Result{}, false, time.Time{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return reservationv1.Result{}, false, time.Time{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return reservationv1.Result{}, false, time.Time{}, errors.New("invalid server state mirror file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return reservationv1.Result{}, false, time.Time{}, err
	}
	result, err := reservationv1.ParseResult(raw)
	if err != nil {
		return reservationv1.Result{}, false, time.Time{}, err
	}
	if result.Schema != reservationv1.StateSchema || result.RepoID != "" || result.ServerID != serverID {
		return reservationv1.Result{}, false, time.Time{}, errors.New("server state mirror identity mismatch")
	}
	return result, true, info.ModTime(), nil
}

func StoreServerState(path, serverID string, result reservationv1.Result, transit ...time.Duration) error {
	if !filepath.IsAbs(path) {
		return errors.New("server state mirror path must be absolute")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	parsed, err := reservationv1.ParseResult(raw)
	if err != nil {
		return err
	}
	if parsed.Schema != reservationv1.StateSchema || parsed.RepoID != "" || parsed.ServerID != serverID {
		return errors.New("broker server identity mismatch")
	}
	if result.StorageCapacity != nil && result.StorageCapacity.State == "observed" {
		copy := *result.StorageCapacity
		elapsed := time.Duration(0)
		if len(transit) > 0 {
			elapsed = transit[0]
		}
		remaining := time.Duration(copy.ValidForSeconds)*time.Second - elapsed
		if elapsed < 0 || remaining < 0 {
			remaining = 0
		}
		copy.ValidForSeconds = int(remaining / time.Second)
		result.StorageCapacity = &copy
	}
	if previous, exists, receipt, err := loadServerStateSnapshot(path, serverID); err == nil && exists {
		old, next := previous.StorageCapacity, result.StorageCapacity
		if old != nil && next != nil && old.State == "observed" && next.State == "observed" && old.MeasuredAt.Equal(*next.MeasuredAt) && old.TotalBytes == next.TotalBytes && old.AvailableBytes == next.AvailableBytes {
			copy := *next
			now := time.Now()
			remaining := receipt.Add(time.Duration(old.ValidForSeconds) * time.Second).Sub(now)
			if remaining < 0 || now.Before(receipt) {
				remaining = 0
			}
			if seconds := int(remaining / time.Second); seconds < copy.ValidForSeconds {
				copy.ValidForSeconds = seconds
			}
			result.StorageCapacity = &copy // unrelated metadata cannot renew a sample
		}
		if result.StorageCapacity == nil || result.StorageCapacity.State == "unknown" {
			if previous.StorageCapacity != nil && previous.StorageCapacity.State == "observed" {
				copy := *previous.StorageCapacity
				copy.ValidForSeconds = 0 // keep last numbers, never restore their freshness
				result.StorageCapacity = &copy
			}
		}
		previousRaw, _ := json.Marshal(previous)
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if string(previousRaw) == string(raw) {
			return nil
		}
	} else {
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
	}
	if err := privatefile.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".server-state-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := privatefile.Harden(temp); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(path))
}

// Read the existing private server mirror. Its file time is the local receipt
// fence; cache reads never write/renew it. Clocks from different hosts are not
// compared. Expired/unsupported readings keep old numbers explicitly stale.
func LoadServerCapacity(path, serverID string, now time.Time) *contract.StorageCapacityStatus {
	unknown := &contract.StorageCapacityStatus{State: "unknown"}
	r, ok, receipt, err := loadServerStateSnapshot(path, serverID)
	if err != nil || !ok || r.StorageCapacity == nil || r.StorageCapacity.State != "observed" {
		return unknown
	}
	s := r.StorageCapacity
	value := &contract.StorageCapacityStatus{State: "stale", TotalBytes: s.TotalBytes, AvailableBytes: s.AvailableBytes, MeasuredAt: s.MeasuredAt.UTC().Format(time.RFC3339Nano)}
	age := now.Sub(receipt)
	value.ExpiresAt = receipt.Add(time.Duration(s.ValidForSeconds) * time.Second).UTC().Format(time.RFC3339Nano)
	if age >= 0 && s.ValidForSeconds > 0 && age < time.Duration(s.ValidForSeconds)*time.Second {
		value.State = "fresh"
	}
	return value
}
