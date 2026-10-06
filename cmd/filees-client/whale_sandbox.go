package main

import (
	"os"
	"path/filepath"

	"filees/internal/obsandbox"
	"filees/pkg/clientprofile"
	"filees/pkg/whaleclient"
)

func whaleSandboxProfile(opt whaleFlags, profile clientprofile.Profile) (obsandbox.Profile, error) {
	paths := []obsandbox.Path{
		{Label: "identity", Name: profile.IdentityFile, Perms: "r"},
		{Label: "known-hosts", Name: profile.KnownHosts, Perms: "r"},
		{Label: "state", Name: opt.state, Perms: "rwc"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		{Label: "random", Name: "/dev/urandom", Perms: "r"},
		{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"},
		{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
	}
	if filepath.IsAbs(opt.source) {
		paths = append(paths, obsandbox.Path{Label: "source", Name: opt.source, Perms: "r"})
	}
	if filepath.IsAbs(opt.dest) {
		paths = append(paths, obsandbox.Path{Label: "destination-parent", Name: filepath.Dir(opt.dest), Perms: "rwc"})
	}
	manager, err := whaleclient.NewManager(opt.state, []clientprofile.Profile{profile})
	if err != nil {
		return obsandbox.Profile{}, err
	}
	operations, err := manager.List()
	if err != nil {
		return obsandbox.Profile{}, err
	}
	for _, op := range operations {
		if op.SourcePath != "" {
			if _, err := os.Lstat(op.SourcePath); err == nil {
				paths = append(paths, obsandbox.Path{Label: "stored-source", Name: op.SourcePath, Perms: "r"})
			}
		}
		if op.SpoolRoot != "" {
			paths = append(paths, obsandbox.Path{Label: "stored-spool", Name: op.SpoolRoot, Perms: "rwc"})
		}
		if op.DestinationPath != "" {
			paths = append(paths, obsandbox.Path{Label: "stored-destination-parent", Name: filepath.Dir(op.DestinationPath), Perms: "rwc"})
		}
	}
	return obsandbox.Profile{Name: "filees-client/whale", Promises: "stdio rpath wpath cpath fattr flock inet dns", Paths: paths}, nil
}
