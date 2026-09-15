package main

import (
	"context"
	"path/filepath"
	"time"

	"filees/pkg/clientprofile"
)

type sessionTimeoutService struct {
	root        string
	provisioner *daemonProvisioner
	onChange    func(clientprofile.Profile)
}

func (s sessionTimeoutService) SetSessionTimeout(_ context.Context, serverID string, minutes int) (int, error) {
	timeout, err := clientprofile.NormalizeSessionTimeout(minutes)
	if err != nil {
		return 0, err
	}
	// ServerDir, not a join: `atmprojekt:filees` lives in `atmprojekt+3Afilees`,
	// and the raw ID is not even a valid path on Windows.
	dir, err := clientprofile.ServerDir(s.root, serverID)
	if err != nil {
		return 0, err
	}
	path := filepath.Join(dir, "client-profile.json")
	profile, err := clientprofile.Load(path)
	if err != nil {
		return 0, err
	}
	profile.SessionTimeout = timeout
	if err := clientprofile.Store(path, profile); err != nil {
		return 0, err
	}
	if s.provisioner != nil {
		s.provisioner.AddProfile(profile)
	}
	if s.onChange != nil {
		s.onChange(profile)
	}
	return int(timeout / time.Minute), nil
}
