package config

import "testing"

func TestLoadUpdateHasSeparatePinnedSSHProfile(t *testing.T) {
	raw := `{"transport":{"identity_file":"/tmp/user-id","known_hosts":"/tmp/user-known"},"update":{"enabled":true,"repo_url":"svn+ssh://release@host/repo","state_path":"/tmp/state","stage_root":"/tmp/stage","ssh":{"identity_file":"/tmp/release-id","known_hosts":"/tmp/release-known","port":2223,"host_name":"release-host"}},"repositories":[]}`
	view, err := LoadClientView(writeRawConfig(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	ssh := view.Update.SSH
	if ssh == nil || ssh.IdentityFile != fixtureAbs("/tmp/release-id") || ssh.KnownHosts != fixtureAbs("/tmp/release-known") || ssh.Port != 2223 || ssh.HostName != "release-host" {
		t.Fatal(ssh)
	}
	// Existing server credentials are not an implicit release identity.
	missing := `{"transport":{"identity_file":"/tmp/user-id","known_hosts":"/tmp/user-known"},"update":{"enabled":true,"repo_url":"svn+ssh://release@host/repo","state_path":"/tmp/state","stage_root":"/tmp/stage"},"repositories":[]}`
	if _, err := LoadClientView(writeRawConfig(t, missing)); err == nil {
		t.Fatal("borrowed repository credentials")
	}
	for _, bad := range []UpdateSSHConfig{{}, {IdentityFile: "relative", KnownHosts: fixtureAbs("/tmp/known")}, {IdentityFile: fixtureAbs("/tmp/id"), KnownHosts: fixtureAbs("/tmp/known"), Port: 65536}} {
		if err := (UpdateConfig{RepoURL: "svn+ssh://release@host/repo", SSH: &bad}).ValidateTransport(); err == nil {
			t.Fatal("invalid profile accepted", bad)
		}
	}
}
