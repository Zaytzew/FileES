package servertool

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"

	"filees/internal/obsandbox"
	"filees/pkg/activation"
	"filees/pkg/deploy"
	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"
)

const (
	ClientSVNCommand     = "svnserve -t"
	ClientControlCommand = "filees control-v1"
	repositoryWorkerPath = "/usr/local/libexec/filees/filees-worker"
	clientEntryPromises  = writePromises + " proc exec"
	// dpath is needed only while the SVN branch's ClaimSession creates its
	// private revoke FIFO with mkfifo(2). The later supervisor profile drops
	// it before relaying the client session.
	clientSVNEntryPromises = writePromises + " dpath proc exec"
)

func RunClientEntry(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return runClientEntry("/etc/filees/server.json", args, stdin, stdout, stderr, getenv, runSVNSessionSupervisor)
}

// clientSVNSupervisor returns the svnserve exit status separately from an
// infrastructure error in the one-shot supervisor.
type clientSVNSupervisor func(serverconfig.Config, string, *activation.Manager, *activation.SessionLease, io.Reader, io.Writer, io.Writer) (int, error)

func runClientEntry(configPath string, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, supervise clientSVNSupervisor) int {
	originalCommand := getenv("SSH_ORIGINAL_COMMAND")
	if len(args) != 2 || (originalCommand != ClientSVNCommand && originalCommand != deploy.ServiceProofCommand && originalCommand != ClientControlCommand) {
		fmt.Fprintln(stderr, "filees-client-entry: rejected command")
		return ExitUnavailable
	}
	entryPromises := clientEntryPromises
	if originalCommand == ClientSVNCommand {
		entryPromises = clientSVNEntryPromises
	}
	if originalCommand == ClientControlCommand {
		// After the repository worker exits, this process drops proc/exec and
		// uses the already locked filesystem profile for one mail attempt.
		entryPromises += " inet dns"
	}
	if err := sandboxBegin(entryPromises); err != nil {
		report(stderr, "filees-client-entry sandbox", err)
		return ExitSoftware
	}
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		report(stderr, "filees-client-entry config", err)
		return ExitConfig
	}
	// Loading the SMTP trust store can touch system paths which must not be
	// added to the broad forced-command unveil profile. Prepare it before
	// unveil is locked, but keep a mail-only failure non-fatal to the durable
	// control operation below.
	var mailConfig serverconfig.Config
	var mailConfigErr error
	if originalCommand == ClientControlCommand {
		mailConfig, mailConfigErr = serverconfig.LoadFor(configPath, serverconfig.SecretSMTP)
	}
	profile := obsandbox.Profile{Name: "filees-client-entry/proof", Promises: entryPromises, Paths: []obsandbox.Path{
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
	if originalCommand == ClientControlCommand {
		r := config.Repositories
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "server-config", Name: configPath, Perms: "r"})
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"}, obsandbox.Path{Label: "hosts", Name: "/etc/hosts", Perms: "r"})
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "null-device", Name: "/dev/null", Perms: "rw"})
		profile.Paths = append(profile.Paths,
			obsandbox.Path{Label: "service-wc-parent", Name: filepath.Dir(config.Activation.ServiceWorkingCopy), Perms: "r"},
			obsandbox.Path{Label: "service-repository-control", Name: config.Activation.ServiceRepository, Perms: "rwc"},
			obsandbox.Path{Label: "svn-system-config", Name: "/etc/subversion", Perms: "r"},
		)
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "repository-worker", Name: repositoryWorkerPath, Perms: "rx"}, obsandbox.Path{Label: "service-wc", Name: config.Activation.ServiceWorkingCopy, Perms: "rwc"}, obsandbox.Path{Label: "repository-root", Name: r.Root, Perms: "rwc"}, obsandbox.Path{Label: "repository-results", Name: r.ResultsRoot, Perms: "rwc"}, obsandbox.Path{Label: "data-authz", Name: r.DataAuthzFile, Perms: "rwc"}, obsandbox.Path{Label: "svnadmin", Name: r.SVNAdminBinary, Perms: "rx"}, obsandbox.Path{Label: "svn", Name: config.Activation.SVNBinary, Perms: "rx"})
		// LOAD_REPOSITORY_DUMP's DumpLoadService shells out to svnlook
		// (always) and svndumpfilter (only when applying the ignore
		// policy); both must be unveiled up front like svnadmin/svn since
		// this profile is locked before the worker is exec'd.
		if svnlook := r.EffectiveSVNLookBinary(); svnlook != "" {
			profile.Paths = append(profile.Paths, obsandbox.Path{Label: "svnlook", Name: svnlook, Perms: "rx"})
		}
		if svndumpfilter := r.EffectiveSVNDumpFilterBinary(); svndumpfilter != "" {
			profile.Paths = append(profile.Paths, obsandbox.Path{Label: "svndumpfilter", Name: svndumpfilter, Perms: "rx"})
		}
		if deletionArchiveNeedsOwnUnveil(r.ResultsRoot, r.DeletionArchiveRoot) {
			profile.Paths = append(profile.Paths, obsandbox.Path{Label: "repository-deletion-archive", Name: r.DeletionArchiveRoot, Perms: "rwc"})
		}
		// Onboarding root + OTP pepper: needed by the exec'd worker to mint
		// mobile pairing tokens (MOBILE_PAIRING ticket, onboarding.Files.
		// CreateMobilePairing) - the same token-hashing discipline already
		// used for admin-issued tickets, just triggered from this
		// authenticated control-plane channel instead of filees-admin.
		profile.Paths = append(profile.Paths, obsandbox.Path{Label: "onboarding-root", Name: config.Root, Perms: "rwc"}, obsandbox.Path{Label: "otp-pepper", Name: config.OTPPepperFile, Perms: "r"})
		childPromises = workerPromises + " dns unveil"
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
		defer func() {
			if closeErr := lease.Close(); closeErr != nil {
				report(stderr, "filees-client-entry session cleanup", closeErr)
			}
		}()
		childExitCode, err := supervise(config, args[1], manager, lease, stdin, stdout, stderr)
		if err != nil {
			report(stderr, "filees-client-entry supervisor", err)
			return ExitSoftware
		}
		return childExitCode
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
		if err := runRepositoryWorkerProcess(config.Repositories.ResultsRoot, filepath.Dir(config.Repositories.SVNAdminBinary), args[1], stdin, stdout, stderr); err != nil {
			report(stderr, "filees-client-entry control", err)
			return ExitSoftware
		}
		// A failure here never rewrites the already durable control result; the
		// outbox is retried by the next server-side action instead of a periodic
		// job. The in-process attempt is required because OpenBSD carries the
		// parent's locked unveil table across exec and forbids replacing it.
		if mailConfigErr != nil {
			report(stderr, "filees-client-entry mail trigger", mailConfigErr)
		} else if err := runMailAfterControl(mailConfig, stderr); err != nil {
			report(stderr, "filees-client-entry mail trigger", err)
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

// svnBinDir becomes the worker's entire $PATH. internal/svnrotate (reused by
// LOAD_REPOSITORY_DUMP) shells out to bare "svnadmin"/"svnlook"/"svn" names,
// unlike DumpLoadService which takes configured absolute paths; PATH lets
// that bare-name lookup resolve without widening what unveil actually
// permits — LookPath still only succeeds for names unveil already allows.
var runRepositoryWorkerProcess = func(tempRoot, svnBinDir, clientID string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.Command(repositoryWorkerPath, "repository-control", clientID)
	command.Env = []string{"TMPDIR=" + tempRoot, "PATH=" + svnBinDir}
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	return command.Run()
}

var runMailAfterControl = deliverMailAfterControl

func deliverMailAfterControl(config serverconfig.Config, stderr io.Writer) error {
	// No child process or second unveil pass is allowed here. Once proc/exec is
	// dropped, the process can only use the paths fixed by the client-entry
	// profile and the network capabilities needed for SMTP.
	if err := sandboxNarrow(mailPromises); err != nil {
		return fmt.Errorf("narrow sandbox for mail: %w", err)
	}
	access := onboarding.Access{Areas: onboarding.AreaTickets | onboarding.AreaOperations | onboarding.AreaAudit}
	if err := onboarding.CheckExisting(config.Root, access); err != nil {
		return fmt.Errorf("prepare mail repository: %w", err)
	}
	files, err := onboarding.OpenPrepared(config.Root, config.Onboarding, access)
	if err != nil {
		return fmt.Errorf("open mail repository: %w", err)
	}
	if code := deliverPendingMail(files, config, io.Discard, stderr); code != ExitOK {
		return fmt.Errorf("mail delivery exited with status %d", code)
	}
	return nil
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
