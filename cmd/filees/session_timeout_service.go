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
	path := filepath.Join(s.root, serverID, "client-profile.json")
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
