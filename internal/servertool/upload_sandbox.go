package servertool

import (
	"path/filepath"

	"filees/internal/obsandbox"
	"filees/pkg/serverconfig"
)

const uploadMaintenancePromises = "stdio rpath wpath cpath fattr flock proc exec unix inet dns"

// Cron has no forced-command parent: it must install its own sandbox before
// reading intake/channel data or invoking SVN and the configured AV scanner.
func uploadMaintenanceProfile(config serverconfig.Config, seed bool) obsandbox.Profile {
	r := config.Repositories
	p := obsandbox.Profile{Name: "filees-worker/upload-maintenance", Promises: writePromises, Paths: []obsandbox.Path{
		{Label: "channel-state", Name: config.PublicShares.EffectiveStateRoot(r.ResultsRoot), Perms: "r"},
		{Label: "quarantine", Name: config.Upload.EffectiveTrashRoot(r.ResultsRoot), Perms: "rwc"},
	}}
	if seed {
		return p
	}
	p.Promises = uploadMaintenancePromises
	p.Paths = append(p.Paths,
		obsandbox.Path{Label: "intake", Name: config.Upload.IntakeRoot, Perms: "rwc"},
		obsandbox.Path{Label: "repositories-parent", Name: filepath.Dir(r.Root), Perms: "r"},
		obsandbox.Path{Label: "repositories", Name: r.Root, Perms: "rwc"},
		obsandbox.Path{Label: "svnmucc", Name: r.EffectiveSVNMuccBinary(), Perms: "rx"},
		obsandbox.Path{Label: "svnlook", Name: r.EffectiveSVNLookBinary(), Perms: "rx"},
		obsandbox.Path{Label: "scanner", Name: config.Upload.AVCommand[0], Perms: "rx"},
		obsandbox.Path{Label: "hooks", Name: repositoryWorkerPath, Perms: "rx"},
		obsandbox.Path{Label: "shell", Name: "/bin/sh", Perms: "rx"},
		obsandbox.Path{Label: "null", Name: "/dev/null", Perms: "rw"},
		obsandbox.Path{Label: "random", Name: "/dev/urandom", Perms: "r"},
		obsandbox.Path{Label: "temporary", Name: "/tmp", Perms: "rwc"},
		obsandbox.Path{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		obsandbox.Path{Label: "hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		obsandbox.Path{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		obsandbox.Path{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
		obsandbox.Path{Label: "svn-config", Name: "/etc/subversion", Perms: "r"},
		obsandbox.Path{Label: "clam-config", Name: "/etc/clamd.conf", Perms: "r"},
		obsandbox.Path{Label: "clam-config-directory", Name: "/etc/clamav", Perms: "r"},
		obsandbox.Path{Label: "clam-socket", Name: "/var/run/clamav", Perms: "rw"},
		obsandbox.Path{Label: "clam-database", Name: "/var/db/clamav", Perms: "r"},
		obsandbox.Path{Label: "clam-library", Name: "/usr/local/share/clamav", Perms: "r"},
		obsandbox.Path{Label: "dns", Name: "/etc/resolv.conf", Perms: "r"},
		obsandbox.Path{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
	)
	return p
}
