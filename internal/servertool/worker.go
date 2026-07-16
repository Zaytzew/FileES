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

	"filees/pkg/activation"
	"filees/pkg/deploy"
	"filees/pkg/onboarding"

	"golang.org/x/crypto/ssh"
)

const WorkerResultSchema = "filees.worker-result/v1"

type WorkerResult struct {
	Schema          string                `json:"schema"`
	Status          string                `json:"status"`
	OperationID     string                `json:"operation_id"`
	ClientID        string                `json:"client_id"`
	Identity        deploy.PublicIdentity `json:"identity"`
	ServiceRevision int64                 `json:"service_revision"`
}

func RunWorker(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWorker("/etc/filees/server.json", args, stdin, stdout, stderr)
}

func runWorker(configPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWorkerWithCheckpoint(configPath, args, stdin, stdout, stderr, nil)
}

type workerCheckpoint func(string) error

func runWorkerWithCheckpoint(configPath string, args []string, stdin io.Reader, stdout, stderr io.Writer, checkpoint workerCheckpoint) int {
	reached := func(name string) bool {
		if checkpoint == nil {
			return true
		}
		if err := checkpoint(name); err != nil {
			report(stderr, "filees-worker checkpoint "+name, err)
			return false
		}
		return true
	}
	if len(args) != 1 || args[0] != "deploy" {
		fmt.Fprintln(stderr, "usage: filees-worker deploy < tunnel-session.json")
		return ExitUsage
	}
	session, err := deploy.DecodeTunnelSession(stdin)
	if err != nil {
		report(stderr, "filees-worker session", err)
		return ExitData
	}
	files, config, err := openFiles(configPath, toolAccess{name: "filees-worker/deploy", areas: onboarding.AreaOperations, write: true, needWorker: true, needWorkerPublic: true, needActivation: true, needSVN: true})
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
	if !reached("helper_announced") {
		return ExitTempFail
	}
	operation, err := files.GetOperation(grant.OperationID)
	if err != nil {
		report(stderr, "filees-worker operation", err)
		return ExitTempFail
	}
	deadline := time.Now().Add(30 * time.Second)
	if grant.ExpiresAt.Before(deadline) {
		deadline = grant.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	request := deploy.HelperRequest{Schema: deploy.HelperSchema, OperationID: grant.OperationID, RequestID: session.DeployRequestID, ClientID: grant.ClientID, Action: deploy.ActionGenerateIdentity}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(grant.AssignedReversePort)))
	identity := deploy.PublicIdentity{PublicKey: operation.InstallationPublicKey, Fingerprint: operation.InstallationFingerprint}
	if operation.State == onboarding.OperationHelperAnnounced {
		response, err := deploy.GenerateIdentityThroughHelper(ctx, address, session.HelperHostPublicKey, config.WorkerSigner, request)
		if err != nil {
			report(stderr, "filees-worker helper", err)
			return ExitTempFail
		}
		identity = *response.Identity
		if err := validatePublicIdentity(identity); err != nil {
			report(stderr, "filees-worker identity", err)
			return ExitData
		}
		if !reached("identity_returned") {
			return ExitTempFail
		}
		if err := files.CompleteGeneratedIdentity(grant.OperationID, session.DeployRequestID, identity.PublicKey, identity.Fingerprint); err != nil {
			report(stderr, "filees-worker publish", err)
			return ExitTempFail
		}
		operation.State = onboarding.OperationIdentityGenerated
		operation.InstallationPublicKey, operation.InstallationFingerprint = identity.PublicKey, identity.Fingerprint
		if !reached("identity_generated") {
			return ExitTempFail
		}
	} else if err := validatePublicIdentity(identity); err != nil {
		report(stderr, "filees-worker persisted identity", err)
		return ExitData
	}
	activationGrant := onboarding.ActivationGrant{
		OperationID: operation.OperationID, ClientID: operation.ClientID, RealmID: operation.ApprovedPolicy.RealmID,
		DeployRequestID: operation.DeployRequestID, InstallationPublicKey: identity.PublicKey,
		InstallationFingerprint: identity.Fingerprint, ExpiresAt: operation.ExpiresAt,
	}
	manager, err := activation.New(config.Activation, nil)
	if err != nil {
		report(stderr, "filees-worker activation", err)
		return ExitConfig
	}
	if operation.State == onboarding.OperationIdentityGenerated {
		if err := manager.Stage(activationGrant); err != nil {
			report(stderr, "filees-worker stage access", err)
			return ExitTempFail
		}
		if !reached("activation_staged") {
			return ExitTempFail
		}
		if err := files.CompleteAccessStaged(grant.OperationID, session.DeployRequestID); err != nil {
			report(stderr, "filees-worker stage state", err)
			return ExitTempFail
		}
		operation.State = onboarding.OperationAccessStaged
		if !reached("access_staged") {
			return ExitTempFail
		}
	}
	if operation.State == onboarding.OperationAccessStaged {
		proofRequest := request
		proofRequest.Action = deploy.ActionProveServiceAccess
		if err := deploy.ProveServiceAccessThroughHelper(ctx, address, session.HelperHostPublicKey, config.WorkerSigner, proofRequest); err != nil {
			report(stderr, "filees-worker proof", err)
			return ExitTempFail
		}
		if err := manager.HasProof(activationGrant); err != nil {
			report(stderr, "filees-worker proof receipt", err)
			return ExitTempFail
		}
		if !reached("possession_receipt") {
			return ExitTempFail
		}
		if err := files.CompletePossessionProof(grant.OperationID, session.DeployRequestID); err != nil {
			report(stderr, "filees-worker proof state", err)
			return ExitTempFail
		}
		operation.State = onboarding.OperationPossessionProved
		if !reached("possession_proved") {
			return ExitTempFail
		}
	}
	revision := operation.ServiceRevision
	if operation.State == onboarding.OperationPossessionProved {
		revision, err = manager.Publish(ctx, activationGrant)
		if err != nil {
			report(stderr, "filees-worker service publish", err)
			return ExitTempFail
		}
		if !reached("service_published") {
			return ExitTempFail
		}
		if err := files.CompleteActivation(grant.OperationID, session.DeployRequestID, revision); err != nil {
			report(stderr, "filees-worker activate", err)
			return ExitTempFail
		}
		operation.State, operation.ServiceRevision = onboarding.OperationActive, revision
		if !reached("operation_active") {
			return ExitTempFail
		}
	}
	if operation.State != onboarding.OperationActive || revision <= 0 {
		fmt.Fprintln(stderr, "filees-worker: operation is not resumable")
		return ExitTempFail
	}
	if err := writeJSON(stdout, WorkerResult{Schema: WorkerResultSchema, Status: "active", OperationID: grant.OperationID, ClientID: grant.ClientID, Identity: identity, ServiceRevision: revision}); err != nil {
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
