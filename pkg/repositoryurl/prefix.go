// Package repositoryurl owns the canonical public locator contract shared by
// server configuration and repository provisioning.
package repositoryurl

import (
	"errors"
	"net/url"
	"strings"
)

// ValidatePrefix rejects a repository URL prefix that could never appear in
// a client projection. The SSH connection port belongs to the installation
// profile, not to the canonical repository identity.
func ValidatePrefix(prefix string) error {
	return validate(prefix, "00000000-0000-4000-8000-000000000000")
}

// Build appends one already-validated opaque repository ID to prefix.
func Build(prefix, repoID string) (string, error) {
	if strings.TrimSpace(repoID) == "" || strings.ContainsAny(repoID, "/\\?#\x00") {
		return "", errors.New("repository ID is invalid")
	}
	if err := validate(prefix, repoID); err != nil {
		return "", err
	}
	return prefix + repoID, nil
}

func validate(prefix, repoID string) error {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(prefix) != prefix || !strings.HasSuffix(prefix, "/") {
		return errors.New("repository URL prefix must be a non-empty restricted svn+ssh URL ending in /")
	}
	parsed, err := url.Parse(prefix + repoID)
	if err != nil || parsed.Scheme != "svn+ssh" || parsed.Hostname() == "" || parsed.User == nil || (parsed.User.Username() != "_filees-client" && parsed.User.Username() != "_filees-data") || parsed.User.String() != parsed.User.Username() || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("repository URL prefix must use restricted svn+ssh transport without an explicit port")
	}
	return nil
}
