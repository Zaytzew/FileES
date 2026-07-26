package servertool

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"filees/internal/obsandbox"
	"filees/pkg/activation"
	"filees/pkg/deploy"
	"filees/pkg/serverconfig"
)

const (
	ClientSVNCommand     = "svnserve -t"
	ClientControlCommand = "filees control-v1"
	repositoryWorkerPath = "/usr/local/libexec/filees/filees-worker"
	clientEntryPromises  = writePromises + " proc exec"
)

func RunClientEntry(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return runClientEntry("/etc/filees/server.json", args, stdin, stdout, stderr, getenv, runSVNSessionSupervisor)
}

type clientSVNSupervisor func(serverconfig.Config, string, *activation.Manager, *activation.SessionLease, io.Reader, io.Writer, io.Writer) error

func runClientEntry(configPath string, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, supervise clientSVNSupervisor) int {
	originalCommand := getenv("SSH_ORIGINAL_COMMAND")
	if len(args) != 2 || (originalCommand != ClientSVNCommand && originalCommand != deploy.ServiceProofCommand && originalCommand != ClientControlCommand) {
		fmt.Fprintln(stderr, "filees-client-entry: rejected command")
		return ExitUnavailable
	}
	if err := sandboxBegin(clientEntryPromises); err != nil {
		report(stderr, "filees-client-entry sandbox", err)
		return ExitSoftware
	}
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		report(stderr, "filees-client-entry config", err)
		return ExitConfig
	}
	profile := obsandbox.Profile{Name: "filees-client-entry/proof", Promises: clientEntryPromises, Paths: []obsandbox.Path{
		{Label: "activation", Name: config.Activation.Root, Perms: "rwc"},
		{Label: "svnserve", Name: config.Activation.SVNServeBinary, Perms: "rx"},
		{Label: "service-repository", Name: config.Activation.ServiceRepository, Perms: "r"},
		{Label: "authz", Name: config.Activation.AuthzFile, Perms: "r"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
		{Label: "sasl-config", Name: "/etc/sasl2", Perms: "r"},
		{Label: "random", Name: "/dev/urandom", Perms: "r"},
	}}
	childPromises := clientChildPromises(originalCommand)
	if originalCommand == ClientSVNCommand && os.Getenv("USER") == "_filees-data" {
		r := config.Repositories
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "repository-root-parent", Name: filepath.Dir(r.Root), Perms: "r"}, obsandbox.Path{Label: "repository-root", Name: r.Root, Perms: "rwc"}, obsandbox.Path{Label: "data-authz", Name: r.DataAuthzFile, Perms: "r"})
	}
	if originalCommand == ClientControlCommand {
		r := config.Repositories
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "server-config", Name: configPath, Perms: "r"})
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "null-device", Name: "/dev/null", Perms: "rw"})
		profile.Paths = append(profile.Paths,
			obsandbox.Path{Label: "service-wc-parent", Name: filepath.Dir(config.Activation.ServiceWorkingCopy), Perms: "r"},
			obsandbox.Path{Label: "service-repository-control", Name: config.Activation.ServiceRepository, Perms: "rwc"},
			obsandbox.Path{Label: "svn-system-config", Name: "/etc/subversion", Perms: "r"},
		)
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "repository-worker", Name: repositoryWorkerPath, Perms: "rx"}, obsandbox.Path{Label: "service-wc", Name: config.Activation.ServiceWorkingCopy, Perms: "rwc"}, obsandbox.Path{Label: "repository-root", Name: r.Root, Perms: "rwc"}, obsandbox.Path{Label: "repository-results", Name: r.ResultsRoot, Perms: "rwc"}, obsandbox.Path{Label: "data-authz", Name: r.DataAuthzFile, Perms: "rwc"}, obsandbox.Path{Label: "svnadmin", Name: r.SVNAdminBinary, Perms: "rx"}, obsandbox.Path{Label: "svn", Name: config.Activation.SVNBinary, Perms: "rx"})
		// Onboarding root + OTP pepper: needed by the exec'd worker to mint
		// mobile pairing tokens (MOBILE_PAIRING ticket, onboarding.Files.
		// CreateMobilePairing) - the same token-hashing discipline already
		// used for admin-issued tickets, just triggered from this
		// authenticated control-plane channel instead of filees-admin.
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "onboarding-root", Name: config.Root, Perms: "rwc"}, obsandbox.Path{Label: "otp-pepper", Name: config.OTPPepperFile, Perms: "r"})
		childPromises = workerPromises + " unveil"
	}
	manager, err := activation.New(config.Activation, nil)
	if err != nil {
		report(stderr, "filees-client-entry activation", err)
		return ExitConfig
	}
	if originalCommand == ClientSVNCommand {
		lease, err := manager.ClaimSession(args[0], args[1])
		if err != nil {
			report(stderr, "filees-client-entry session", err)
			return ExitUnavailable
		}
		defer func() { _ = lease.Close() }()
		if err := supervise(config, args[1], manager, lease, stdin, stdout, stderr); err != nil {
			report(stderr, "filees-client-entry supervisor", err)
			return ExitSoftware
		}
		return ExitOK
	}
	if err := sandboxApplyForExec(profile, childPromises); err != nil {
		report(stderr, "filees-client-entry sandbox", err)
		return ExitSoftware
	}
	if err := manager.RecordProof(args[0], args[1]); err != nil {
		report(stderr, "filees-client-entry proof", err)
		return ExitUnavailable
	}
	if originalCommand == deploy.ServiceProofCommand {
		return ExitOK
	}
	if originalCommand == ClientControlCommand {
		if err := execRepositoryWorker(config.Repositories.ResultsRoot, args[1]); err != nil {
			report(stderr, "filees-client-entry control", err)
			return ExitSoftware
		}
		return ExitOK
	}
	return ExitUnavailable
}

func clientChildPromises(originalCommand string) string {
	if originalCommand == ClientSVNCommand {
		// svnserve needs write/create/attribute promises for authenticated data
		// repositories. Unveil and authz still constrain the exact filesystem
		// roots and effective per-repository access.
		return svnExecPromises
	}
	return "stdio rpath flock prot_exec"
}

var execRepositoryWorker = func(tempRoot, clientID string) error {
	return syscall.Exec(repositoryWorkerPath, []string{"filees-worker", "repository-control", clientID}, []string{"TMPDIR=" + tempRoot})
}

func clientSVNArgs(config serverconfig.Config, clientID, loginUser string) []string {
	root := config.Activation.ServiceRepository
	if loginUser == "_filees-data" {
		root = config.Repositories.Root
	}
	return []string{
		filepath.Base(config.Activation.SVNServeBinary), "-t", "--tunnel-user", clientID,
		"-r", root,
	}
}
