package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	InstalledRelease string        `json:"installed_release,omitempty"`
	InstalledAt      string        `json:"installed_at,omitempty"`
	System           *SystemState  `json:"system,omitempty"`
	History          []HistoryEntry `json:"history,omitempty"`
}

// SystemState records what the first-install system tasks created.
// Written once on first apply; not modified on upgrade.
// purge uses it to undo exactly what was set up.
type SystemState struct {
	UsersCreated []string `json:"users_created,omitempty"`
	LoginConf    string   `json:"login_conf,omitempty"`
	SSHDFragment string   `json:"sshd_fragment,omitempty"`
	SSHDBackup   string   `json:"sshd_backup,omitempty"`
	SetuidBins   []string `json:"setuid_bins,omitempty"`
}

type HistoryEntry struct {
	ReleaseID       string       `json:"release_id"`
	PreviousRelease string       `json:"previous_release,omitempty"`
	InstalledAt     string       `json:"installed_at"`
	BackupDir       string       `json:"backup_dir"`
	Files           []BackupFile `json:"files"`
}

type BackupFile struct {
	Target       string `json:"target"`
	BackupPath   string `json:"backup_path,omitempty"`
	Existed      bool   `json:"existed"`
	SHA256Before string `json:"sha256_before,omitempty"`
}

// IsFirstInstall returns true when no prior apply has recorded system state.
func (s *State) IsFirstInstall() bool { return s.System == nil }

func Path(dir string) string { return filepath.Join(dir, "state.json") }

func Load(dir string) (*State, error) {
	data, err := os.ReadFile(Path(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{}, nil
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func Save(dir string, st *State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := Path(dir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(dir))
}

func NowStamp() string {
	return time.Now().UTC().Format("20060102T150405Z")
}
