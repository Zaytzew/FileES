package client

import "errors"

// PinnedSSHEnvironment reuses the desktop tunnel policy for release fetching.
// Never borrow the user's SSH agent, ambient SVN_SSH or per-user SSH config.
func PinnedSSHEnvironment(parent []string, identity, knownHosts string, port int, host string) ([]string, error) {
	command := buildSSHCommand(identity, knownHosts, port, host)
	if command == "" {
		return nil, errors.New("SVN SSH transport requires a valid identity and pinned known_hosts profile")
	}
	return svnProcessEnvironment(parent, command), nil
}
