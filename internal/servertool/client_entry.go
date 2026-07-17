package servertool

import (
	"fmt"
	"io"
	"path/filepath"
	"syscall"

	"filees/internal/obsandbox"
	"filees/pkg/activation"
	"filees/pkg/deploy"
	"filees/pkg/serverconfig"
)

const (
	ClientSVNCommand    = "svnserve -t"
	clientEntryPromises = writePromises + " proc exec"
)

func RunClientEntry(args []string, _ io.Reader, _ io.Writer, stderr io.Writer, getenv func(string) string) int {
	return runClientEntry("/etc/filees/server.json", args, stderr, getenv, execClientSVN)
}

func runClientEntry(configPath string, args []string, stderr io.Writer, getenv func(string) string, execute func(serverconfig.Config, string) error) int {
	originalCommand := getenv("SSH_ORIGINAL_COMMAND")
	if len(args) != 2 || (originalCommand != ClientSVNCommand && originalCommand != deploy.ServiceProofCommand) {
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
	if err := sandboxApplyForExec(profile, "stdio rpath flock prot_exec"); err != nil {
		report(stderr, "filees-client-entry sandbox", err)
		return ExitSoftware
	}
	manager, err := activation.New(config.Activation, nil)
	if err != nil {
		report(stderr, "filees-client-entry activation", err)
		return ExitConfig
	}
	if err := manager.RecordProof(args[0], args[1]); err != nil {
		report(stderr, "filees-client-entry proof", err)
		return ExitUnavailable
	}
	if originalCommand == deploy.ServiceProofCommand {
		return ExitOK
	}
	if err := execute(config, args[1]); err != nil {
		report(stderr, "filees-client-entry exec", err)
		return ExitSoftware
	}
	return ExitOK
}

func execClientSVN(config serverconfig.Config, clientID string) error {
	return syscall.Exec(config.Activation.SVNServeBinary, []string{
		filepath.Base(config.Activation.SVNServeBinary), "-t", "--tunnel-user", clientID,
		"-r", config.Activation.ServiceRepository,
	}, []string{})
}
