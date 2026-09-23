// Package predecessor lets one FileES desktop variant take over from the other
// on the same Windows machine: the MSI install and the Microsoft Store package.
//
// The two cannot run side by side. They share the user's daemon socket, the
// global single-instance lock and the state under ~/.local/share/filees, and
// each brings its own autostart, so whichever starts second fails - and until
// this package existed it failed either as an English refusal (Store) or not
// at all, with the MSI supervisor adopting the Store daemon as its own.
//
// The owner's rule (2026-09-23) is deliberately small: no migration machinery,
// the variant being started removes the other and offers to keep its settings.
// "Settings" is config.json and nothing else. Transport identity, activations
// and working copies live in the shared state directory and are never touched,
// which is what makes the takeover cheap.
package predecessor

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
)

// Kind names the variant being replaced.
type Kind string

const (
	KindMSI   Kind = "msi"
	KindStore Kind = "store"
)

// ParseKind accepts only the two variants that exist.
func ParseKind(value string) (Kind, error) {
	switch Kind(value) {
	case KindMSI, KindStore:
		return Kind(value), nil
	}
	return "", fmt.Errorf("unknown FileES variant %q (want msi or store)", value)
}

// CarriedSettings describes what CarrySettings did, so the caller can tell the
// user where the recovery copies are instead of asserting that nothing was lost.
type CarriedSettings struct {
	// Backup is the directory holding the copies taken before anything changed.
	Backup string
	// Carried is false when the predecessor had no configuration to carry; that
	// is a fresh predecessor, not an error.
	Carried bool
	// ReplacedTarget is true when the target already had a configuration, which
	// is kept in Backup as previous-target-config.json.
	ReplacedTarget bool
}

// CarrySettings copies the predecessor's configuration to target.
//
// Both files are copied into a new directory under backupRoot first and read
// back byte for byte, so the recovery copy is known to be exact before the
// target is written. validate is the target variant's own `config-check`: the file has to
// be acceptable to the program that will read it, not merely well-formed JSON.
// If validation fails the target is returned to exactly what it was - the
// previous file, or no file - and the error says so.
func CarrySettings(source, target, backupRoot string, now time.Time, validate func(path string) error) (CarriedSettings, error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(target) || !filepath.IsAbs(backupRoot) {
		return CarriedSettings{}, errors.New("settings paths must be absolute")
	}
	if filepath.Clean(source) == filepath.Clean(target) {
		return CarriedSettings{}, errors.New("predecessor and target configuration are the same file")
	}
	data, err := readRegular(source)
	if errors.Is(err, os.ErrNotExist) {
		return CarriedSettings{}, nil
	}
	if err != nil {
		return CarriedSettings{}, fmt.Errorf("read predecessor configuration: %w", err)
	}
	previous, err := readRegular(target)
	hadTarget := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CarriedSettings{}, fmt.Errorf("read current configuration: %w", err)
	}

	backup, err := newBackupDir(backupRoot, now)
	if err != nil {
		return CarriedSettings{}, err
	}
	result := CarriedSettings{Backup: backup, ReplacedTarget: hadTarget}
	if err := writeVerified(filepath.Join(backup, "predecessor-config.json"), data); err != nil {
		return result, fmt.Errorf("back up predecessor configuration: %w", err)
	}
	if hadTarget {
		if err := writeVerified(filepath.Join(backup, "previous-target-config.json"), previous); err != nil {
			return result, fmt.Errorf("back up current configuration: %w", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return result, fmt.Errorf("prepare configuration directory: %w", err)
	}
	if err := writeAtomic(target, data); err != nil {
		return result, fmt.Errorf("write configuration: %w", err)
	}
	if validate != nil {
		if verr := validate(target); verr != nil {
			var restoreErr error
			if hadTarget {
				restoreErr = writeAtomic(target, previous)
			} else {
				restoreErr = os.Remove(target)
			}
			if restoreErr != nil {
				return result, fmt.Errorf("predecessor configuration is not valid here (%v), and restoring the previous state failed: %w; copies are in %s", verr, restoreErr, backup)
			}
			return result, fmt.Errorf("predecessor configuration is not valid here, nothing was changed: %w", verr)
		}
	}
	result.Carried = true
	return result, nil
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

// newBackupDir never reuses a directory: two takeovers in the same second must
// not overwrite each other's recovery copies.
func newBackupDir(root string, now time.Time) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("prepare backup directory: %w", err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	dir := filepath.Join(root, now.UTC().Format("20060102-150405")+"-"+hex.EncodeToString(suffix))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	return dir, nil
}

func writeVerified(path string, data []byte) error {
	if err := writeAtomic(path, data); err != nil {
		return err
	}
	written, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(written, data) {
		return fmt.Errorf("copy at %s differs from its source", path)
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".filees-config-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}
