package deploy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ServerProfile is user-selected installation policy. It chooses where a
// FileES protocol is reached and which known_hosts file pins that endpoint;
// protocol users, commands and the release bootstrap key remain compiled in.
type ServerProfile struct {
	ID             string
	Address        string
	KnownHostsPath string
}

func profileStateRoot(base string, profile ServerProfile) (string, error) {
	if err := profile.validate(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("server state root must be absolute")
	}
	root := filepath.Join(filepath.Clean(base), profile.ID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create server state root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", fmt.Errorf("secure server state root: %w", err)
	}
	return root, nil
}

func (p ServerProfile) validate() error {
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" || strings.ContainsAny(p.ID, "/\\\x00\r\n\t ") || p.ID == "." || p.ID == ".." {
		return errors.New("server profile ID is invalid")
	}
	if strings.TrimSpace(p.Address) != p.Address || strings.ContainsAny(p.Address, "\x00\r\n\t ") {
		return errors.New("server address is invalid")
	}
	host, port, err := net.SplitHostPort(p.Address)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("server address must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("server port is invalid")
	}
	knownHosts := filepath.Clean(strings.TrimSpace(p.KnownHostsPath))
	if knownHosts == "." || !filepath.IsAbs(knownHosts) {
		return errors.New("server known_hosts path must be absolute")
	}
	return nil
}

func (p ServerProfile) hostAndPort() (string, string) {
	host, port, _ := net.SplitHostPort(p.Address)
	return host, port
}
