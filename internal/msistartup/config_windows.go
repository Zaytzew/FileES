//go:build windows

package msistartup

import (
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

var ErrCancelled = errors.New("initial setup or distribution replacement was cancelled")

// EnsureConfig preserves existing bytes. Publish is no-replace: concurrent
// configuration creation wins over the provisional channel-selection file.
func EnsureConfig(path, home string, choose func(string) error) error {
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() {
			return errors.New("configuration is not a regular file")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := filepath.Join(home, ".local", "share", "filees")
	seed := map[string]any{"transport": map[string]string{
		"identity_file": filepath.Join(state, "identity", "id_ed25519"),
		"known_hosts":   filepath.Join(state, "known_hosts"),
	}, "repositories": []any{}}
	raw, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".filees-setup-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(raw, '\n'))
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err := choose(f.Name()); err != nil {
		return err
	}
	raw, err = os.ReadFile(f.Name())
	if err != nil {
		return err
	}
	var selected struct {
		Update struct {
			Enabled bool   `json:"enabled"`
			Channel string `json:"channel"`
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &selected); err != nil {
		return err
	}
	if !selected.Update.Enabled || (selected.Update.Channel != "alpha" && selected.Update.Channel != "beta") {
		return fmt.Errorf("initial update channel was not saved")
	}
	from, err := windows.UTF16PtrFromString(f.Name())
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func ResolveStore(show bool, present func() (bool, error), ask func() error) (bool, error) {
	installed, err := present()
	if err != nil || !installed {
		return !installed && err == nil, err
	}
	if !show {
		return false, nil
	}
	if err := ask(); err != nil {
		if errors.Is(err, ErrCancelled) {
			return false, nil
		}
		return false, err
	}
	installed, err = present()
	if err != nil {
		return false, err
	}
	if installed {
		return false, errors.New("Microsoft Store package remains installed; refusing a second pair")
	}
	return true, nil
}
