package main

import (
	"os"
	"path/filepath"
	"runtime"

	"filees/internal/obsandbox"
	"filees/pkg/clientprofile"
)

func dumpSandboxProfile(opt dumpFlags, profile clientprofile.Profile) (obsandbox.Profile, error) {
	cfg := Config{SVN: opt.svn, SSH: opt.ssh, Identity: profile.IdentityFile,
		KnownHosts: profile.KnownHosts, Host: profile.Address, Port: profile.SSHPort,
		LogDir: opt.state, Copies: map[string]workingCopySpec{"carrier": {
			ID: "carrier", Path: filepath.Join(opt.state, "carrier"), URL: profile.ServiceURL,
		}}}
	sandbox, err := publishProfile(cfg)
	if err != nil {
		return obsandbox.Profile{}, err
	}
	sandbox.Name = "filees-client/dump-load"
	sandbox.Paths = append(sandbox.Paths,
		obsandbox.Path{Label: "dump-state", Name: opt.state, Perms: "rwc"},
		obsandbox.Path{Label: "new-wc-parent", Name: filepath.Dir(opt.target), Perms: "rwc"},
	)
	if _, err := os.Lstat(opt.source); err == nil {
		sandbox.Paths = append(sandbox.Paths, obsandbox.Path{Label: "source-dump", Name: opt.source, Perms: "r"})
	}
	if runtime.GOOS == "openbsd" {
		for parent := filepath.Dir(filepath.Dir(opt.target)); parent != "/" && parent != "."; parent = filepath.Dir(parent) {
			sandbox.Paths = append(sandbox.Paths, obsandbox.Path{Label: "new-wc-ancestor", Name: parent, Perms: "r"})
		}
	}
	return sandbox, nil
}
