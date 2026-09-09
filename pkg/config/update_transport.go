package config

import (
	"errors"
	"path/filepath"
	"strings"
)

// UpdateSSHConfig is an explicit distribution transport identity. It is not
// inherited from any repository/server activation and cannot override trust
// in the compiled release-signing key.
type UpdateSSHConfig struct {
	IdentityFile string `json:"identity_file"`
	KnownHosts   string `json:"known_hosts"`
	Port         int    `json:"port,omitempty"`
	HostName     string `json:"host_name,omitempty"`
}

func (c UpdateConfig) ValidateTransport() error {
	if c.SSH == nil {
		if strings.HasPrefix(c.RepoURL, "svn+ssh://") {
			return errors.New("config.update.ssh: explicit pinned transport profile required")
		}
		return nil
	}
	for _, path := range []string{c.SSH.IdentityFile, c.SSH.KnownHosts} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, " \t\r\n\x00") {
			return errors.New("config.update.ssh: absolute identity and known_hosts paths without whitespace required")
		}
	}
	if c.SSH.Port < 0 || c.SSH.Port > 65535 || strings.ContainsAny(c.SSH.HostName, " \t\r\n\x00") {
		return errors.New("config.update.ssh: invalid host or port")
	}
	return nil
}
