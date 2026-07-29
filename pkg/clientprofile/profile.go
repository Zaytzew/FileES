// Package clientprofile persists one activated server attachment. The file is
// local client knowledge; server-owned repository authority remains in view.json.
package clientprofile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/internal/durable"

	"github.com/google/uuid"
)

const Schema = "filees.client-profile/v1"

func DefaultRoot() string {
	if root := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); filepath.IsAbs(root) {
		return filepath.Join(filepath.Clean(root), "filees", "servers")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "filees", "servers")
}

type Profile struct {
	Schema           string        `json:"schema"`
	ServerID         string        `json:"server_id"`
	DisplayName      string        `json:"display_name"`
	Address          string        `json:"address"`
	ClientID         string        `json:"client_id"`
	IdentityFile     string        `json:"identity_file"`
	KnownHosts       string        `json:"known_hosts"`
	SSHPort          int           `json:"ssh_port"`
	ServiceURL       string        `json:"service_url"`
	ServiceWC        string        `json:"service_working_copy"`
	RelativeViewPath string        `json:"relative_view_path"`
	CachePath        string        `json:"cache_path"`
	PollInterval     time.Duration `json:"-"`
}

type wireProfile struct {
	Profile
	PollInterval string `json:"poll_interval"`
}

func (profile Profile) Validate() error {
	if profile.Schema != Schema || strings.TrimSpace(profile.ServerID) == "" || strings.ContainsAny(profile.ServerID, "/\\\x00\r\n\t ") {
		return errors.New("client profile schema or server ID is invalid")
	}
	if _, err := uuid.Parse(profile.ClientID); err != nil {
		return errors.New("client profile client ID must be UUID")
	}
	for _, path := range []string{profile.IdentityFile, profile.KnownHosts, profile.ServiceWC, profile.CachePath} {
		if !filepath.IsAbs(path) {
			return errors.New("client profile paths must be absolute")
		}
	}
	if profile.SSHPort < 1 || profile.SSHPort > 65535 || profile.PollInterval <= 0 || strings.TrimSpace(profile.Address) == "" || strings.TrimSpace(profile.ServiceURL) == "" {
		return errors.New("client profile transport is invalid")
	}
	rel := filepath.Clean(profile.RelativeViewPath)
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("client profile view path is invalid")
	}
	return nil
}

func Store(path string, profile Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(wireProfile{Profile: profile, PollInterval: profile.PollInterval.String()}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profile-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(path))
}

func Load(path string) (Profile, error) {
	file, err := os.Open(path)
	if err != nil {
		return Profile{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var wire wireProfile
	if err := decoder.Decode(&wire); err != nil {
		return Profile{}, fmt.Errorf("decode client profile: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Profile{}, errors.New("client profile contains trailing data")
	}
	interval, err := time.ParseDuration(strings.TrimSpace(wire.PollInterval))
	if err != nil || interval <= 0 {
		return Profile{}, errors.New("client profile poll interval is invalid")
	}
	profile := wire.Profile
	profile.PollInterval = interval
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// List discovers only complete profile documents one directory below root.
// Other activation artifacts are ignored; malformed profile files fail closed.
func List(root string) ([]Profile, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("client profile root must be absolute")
	}
	entries, err := os.ReadDir(filepath.Clean(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0, len(entries))
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "client-profile.json")
		profile, err := Load(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load client profile %s: %w", entry.Name(), err)
		}
		if profile.ServerID != entry.Name() {
			return nil, fmt.Errorf("client profile directory does not match server ID %q", profile.ServerID)
		}
		if _, exists := seen[profile.ServerID]; exists {
			return nil, fmt.Errorf("client profile %q is duplicated", profile.ServerID)
		}
		seen[profile.ServerID] = struct{}{}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ServerID < profiles[j].ServerID })
	return profiles, nil
}

// Remove permanently removes one server's profile directory, including its
// private key, projection checkout and cache.
func Remove(root, serverID string) error {
	cleanRoot := filepath.Clean(root)
	if !filepath.IsAbs(cleanRoot) || strings.TrimSpace(serverID) == "" || strings.ContainsAny(serverID, "/\\\x00\r\n\t ") {
		return errors.New("client profile root or server ID is invalid")
	}
	target := filepath.Join(cleanRoot, serverID)
	if filepath.Dir(target) != cleanRoot {
		return errors.New("client profile path escapes root")
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return durable.SyncDirectory(cleanRoot)
}
