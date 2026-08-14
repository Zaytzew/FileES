package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
)

type State struct {
	InstalledRelease string         `json:"installed_release,omitempty"`
	InstalledAt      string         `json:"installed_at,omitempty"`
	HighestSequence  uint64         `json:"highest_sequence,omitempty"`
	SecurityEpoch    uint64         `json:"security_epoch,omitempty"`
	System           *SystemState   `json:"system,omitempty"`
	History          []HistoryEntry `json:"history,omitempty"`
}

// ErrRollback marks a refusal caused by release freshness rather than by a
// malformed or unreadable release.
var ErrRollback = errors.New("release rollback refused")

// CheckFreshness refuses a release that is not at least as new as what this
// machine already installed. A valid signature proves authenticity, never
// freshness: without this an attacker able to serve repository content (or to
// replay it over an unauthenticated transport) can push a genuine older release
// carrying a known vulnerability and it installs as an ordinary update. The
// desktop client has enforced this since internal/clientupdate/state.go; the
// server installer never got the equivalent.
//
// The state backing this check lives in the installer's own state directory,
// never in the staging directory, and advances only after an install succeeds.
func (s *State) CheckFreshness(sequence, securityEpoch uint64, releaseID string) error {
	if s == nil {
		return errors.New("installer state is nil")
	}
	if sequence == 0 || securityEpoch == 0 {
		return fmt.Errorf("%w: release carries no sequence/security_epoch", ErrRollback)
	}
	if securityEpoch < s.SecurityEpoch {
		return fmt.Errorf("%w: security epoch %d is older than the installed %d", ErrRollback, securityEpoch, s.SecurityEpoch)
	}
	if sequence < s.HighestSequence {
		return fmt.Errorf("%w: sequence %d is older than the installed %d", ErrRollback, sequence, s.HighestSequence)
	}
	// The same sequence under a different release_id means two artifacts claim
	// one position in the ordering: a forked or forged release.
	if sequence == s.HighestSequence && s.InstalledRelease != "" && releaseID != s.InstalledRelease {
		return fmt.Errorf("%w: releases %q and %q both claim sequence %d", ErrRollback, s.InstalledRelease, releaseID, sequence)
	}
	return nil
}

// AdvanceFreshness records a successful install. It never moves a counter
// backwards, so an interrupted or partial write can only leave the machine more
// conservative, never less.
func (s *State) AdvanceFreshness(sequence, securityEpoch uint64) {
	if sequence > s.HighestSequence {
		s.HighestSequence = sequence
	}
	if securityEpoch > s.SecurityEpoch {
		s.SecurityEpoch = securityEpoch
	}
}

// SystemState records what the first-install system tasks created.
// Written once on first apply; not modified on upgrade.
// purge uses it to undo exactly what was set up.
type SystemState struct {
	// Adopted marks a pre-existing installation which was verified against a
	// signed manifest. The installer did not create its users or system
	// configuration, so purge must not claim ownership of those resources.
	Adopted      bool     `json:"adopted,omitempty"`
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
	ModeBefore   string `json:"mode_before,omitempty"`
	UIDBefore    *int   `json:"uid_before,omitempty"`
	GIDBefore    *int   `json:"gid_before,omitempty"`
}

// IsFirstInstall returns true when no prior apply has recorded system state.
func (s *State) IsFirstInstall() bool { return s.System == nil }

// CanAdopt is deliberately stricter than IsFirstInstall. Adoption establishes
// the anti-rollback floor exactly once; allowing it over partial or existing
// state would let an operator accidentally discard install history.
func (s *State) CanAdopt() bool {
	return s != nil && s.InstalledRelease == "" && s.InstalledAt == "" &&
		s.HighestSequence == 0 && s.SecurityEpoch == 0 && s.System == nil &&
		len(s.History) == 0
}

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
	// Random temporary name rather than "<path>.tmp": the predictable form is
	// the same symlink-following defect the audit recorded as Finding D for
	// pkg/watcher and pkg/commit, and this file is written by root.
	temp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmp := temp.Name()
	defer os.Remove(tmp)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
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
	if err := os.Rename(tmp, Path(dir)); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}

func NowStamp() string {
	return time.Now().UTC().Format("20060102T150405Z")
}
