package servertool

import (
	"errors"
	"fmt"
	"io"
	"os"

	"filees/pkg/onboarding"
)

const TunnelCommand = "filees tunnel-v1"

type TunnelAccepted struct {
	Schema      string `json:"schema"`
	Status      string `json:"status"`
	OperationID string `json:"operation_id"`
	ReversePort uint16 `json:"reverse_port"`
}

func RunEntry(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) != 0 || getenv("SSH_ORIGINAL_COMMAND") != TunnelCommand {
		fmt.Fprintln(stderr, "filees-entry: rejected command")
		return ExitUnavailable
	}
	files, config, err := openFiles("/etc/filees/server.json", toolAccess{name: "filees-entry/tunnel", areas: onboarding.AreaOperations, write: true})
	if err != nil {
		report(stderr, "filees-entry config", err)
		return ExitConfig
	}
	// S2 deliberately selects the one policy enforceable by unmodified sshd:
	// one fixed loopback listen port and serialized rare onboarding sessions.
	port := config.Onboarding.ReversePortFirst
	if port == 0 || port != config.Onboarding.ReversePortLast {
		fmt.Fprintln(stderr, "filees-entry: configured reverse port is not the fixed SSH port")
		return ExitConfig
	}
	grant, err := files.ClaimAuthorizedTunnel(port)
	if err != nil {
		fmt.Fprintln(stderr, "filees-entry: authentication context does not match an active operation")
		return ExitUnavailable
	}
	if err := writeJSON(stdout, TunnelAccepted{Schema: "filees.ssh-tunnel/v1", Status: "authenticated", OperationID: grant.OperationID, ReversePort: grant.AssignedReversePort}); err != nil {
		return ExitSoftware
	}
	// This process belongs to the SSH session and is not a daemon. In S3 this
	// wait is replaced by the helper probe and exec of the one-shot worker.
	_, err = io.Copy(io.Discard, stdin)
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return ExitTempFail
	}
	return ExitOK
}
