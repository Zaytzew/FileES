package reservationclient

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"filees/internal/durable"
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
	if !filepath.IsAbs(path) {
		return reservationv1.Result{}, false, errors.New("server state mirror path must be absolute")
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return reservationv1.Result{}, false, nil
	}
	if err != nil {
		return reservationv1.Result{}, false, err
	}
	result, err := reservationv1.ParseResult(raw)
	if err != nil {
		return reservationv1.Result{}, false, err
	}
	if result.Schema != reservationv1.StateSchema || result.RepoID != "" || result.ServerID != serverID {
		return reservationv1.Result{}, false, errors.New("server state mirror identity mismatch")
	}
	return result, true, nil
}

func StoreServerState(path, serverID string, result reservationv1.Result) error {
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
	if previous, exists, err := LoadServerState(path, serverID); err == nil && exists && previous.ServerDisplayName == result.ServerDisplayName {
		return nil // the name is unchanged; view age is not a change to this fact
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
