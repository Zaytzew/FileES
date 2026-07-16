package servertool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"filees/pkg/deploy"
	"filees/pkg/onboarding"

	"golang.org/x/crypto/ssh"
)

const WorkerResultSchema = "filees.worker-result/v1"

type WorkerResult struct {
	Schema      string                `json:"schema"`
	Status      string                `json:"status"`
	OperationID string                `json:"operation_id"`
	ClientID    string                `json:"client_id"`
	Identity    deploy.PublicIdentity `json:"identity"`
}

func RunWorker(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWorker("/etc/filees/server.json", args, stdin, stdout, stderr)
}

func runWorker(configPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "deploy" {
		fmt.Fprintln(stderr, "usage: filees-worker deploy < tunnel-session.json")
		return ExitUsage
	}
	session, err := deploy.DecodeTunnelSession(stdin)
	if err != nil {
		report(stderr, "filees-worker session", err)
		return ExitData
	}
	files, config, err := openFiles(configPath, toolAccess{name: "filees-worker/deploy", areas: onboarding.AreaOperations, write: true, needWorker: true, needWorkerPublic: true})
	if err != nil {
		report(stderr, "filees-worker config", err)
		return ExitConfig
	}
	port := config.Onboarding.ReversePortFirst
	if port == 0 || port != config.Onboarding.ReversePortLast {
		fmt.Fprintln(stderr, "filees-worker: configured reverse port is not fixed")
		return ExitConfig
	}
	if !workerKeypairMatches(config.WorkerPublicKey, config.WorkerSigner) {
		fmt.Fprintln(stderr, "filees-worker: configured worker keypair does not match")
		return ExitConfig
	}
	grant, err := files.ClaimAuthorizedHelper(port, session.DeployRequestID, session.HelperHostPublicKey)
	if err != nil {
		fmt.Fprintln(stderr, "filees-worker: no unique authorized tunnel grant")
		return ExitUnavailable
	}
	deadline := time.Now().Add(30 * time.Second)
	if grant.ExpiresAt.Before(deadline) {
		deadline = grant.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	request := deploy.HelperRequest{Schema: deploy.HelperSchema, OperationID: grant.OperationID, RequestID: session.DeployRequestID, ClientID: grant.ClientID, Action: deploy.ActionGenerateIdentity}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(grant.AssignedReversePort)))
	response, err := deploy.GenerateIdentityThroughHelper(ctx, address, session.HelperHostPublicKey, config.WorkerSigner, request)
	if err != nil {
		report(stderr, "filees-worker helper", err)
		return ExitTempFail
	}
	if err := validatePublicIdentity(*response.Identity); err != nil {
		report(stderr, "filees-worker identity", err)
		return ExitData
	}
	if err := files.CompleteGeneratedIdentity(grant.OperationID, session.DeployRequestID, response.Identity.PublicKey, response.Identity.Fingerprint); err != nil {
		report(stderr, "filees-worker publish", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, WorkerResult{Schema: WorkerResultSchema, Status: "identity_generated", OperationID: grant.OperationID, ClientID: grant.ClientID, Identity: *response.Identity}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

func workerKeypairMatches(public string, signer ssh.Signer) bool {
	configuredPublic, _, _, _, err := ssh.ParseAuthorizedKey([]byte(public))
	return err == nil && signer != nil && configuredPublic.Type() == signer.PublicKey().Type() && bytes.Equal(configuredPublic.Marshal(), signer.PublicKey().Marshal())
}

func validatePublicIdentity(identity deploy.PublicIdentity) error {
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(identity.PublicKey)))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return errors.New("helper returned an invalid installation public key")
	}
	if comment == "" || ssh.FingerprintSHA256(key) != identity.Fingerprint {
		return errors.New("helper returned a mismatched installation fingerprint")
	}
	return nil
}
