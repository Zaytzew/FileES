package servertool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"
)

// MobileOnboardPushPayload is the wire shape a mobile device sends over
// MobileOnboardCommand (internal/servertool/entry.go), immediately after its
// pairing token authenticated the outer SSH session (same _filees-tunnel
// BSD-Auth class desktop uses, AuthenticateOTP unchanged) - the installation
// public key is not secret, so it is simply pushed as a payload, not
// fetched by the server through a reverse tunnel. See
// concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md SS4.2.
type MobileOnboardPushPayload struct {
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

type mobileWorkerResult struct {
	Schema          string `json:"schema"`
	Status          string `json:"status"`
	OperationID     string `json:"operation_id,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	ServiceRevision int64  `json:"service_revision,omitempty"`
}

const mobileWorkerResultSchema = "filees.mobile-worker-result/v1"

// RunMobileOnboardWorker is exec'd by execMobileOnboardWorker
// (internal/servertool/entry.go) once a mobile pairing token has
// authenticated the outer SSH session. It claims the one pending pairing
// operation and stages the pushed installation key - the mobile equivalent
// of the desktop deploy worker's identity_generated -> access_staged step,
// minus the entire reverse-tunnel dance desktop needs and mobile does not.
func RunMobileOnboardWorker(stdin io.Reader, stdout, stderr io.Writer) int {
	return runMobileOnboardWorker("/etc/filees/server.json", stdin, stdout, stderr)
}

func runMobileOnboardWorker(configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	var payload MobileOnboardPushPayload
	if err := json.NewDecoder(io.LimitReader(stdin, 64*1024)).Decode(&payload); err != nil {
		report(stderr, "filees-worker mobile-onboard payload", err)
		return ExitUsage
	}
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation|serverconfig.SecretOTP)
	if err != nil {
		report(stderr, "filees-worker mobile-onboard config", err)
		return ExitConfig
	}
	files, err := onboarding.OpenPrepared(config.Root, config.Onboarding, onboarding.Access{Areas: onboarding.AreaOperations | onboarding.AreaAudit, NeedOTP: true})
	if err != nil {
		report(stderr, "filees-worker mobile-onboard onboarding", err)
		return ExitConfig
	}
	grant, err := files.ClaimAuthorizedMobilePush(payload.PublicKey, payload.Fingerprint)
	if err != nil {
		report(stderr, "filees-worker mobile-onboard claim", err)
		return ExitTempFail
	}
	manager, err := activation.New(config.Activation, nil)
	if err != nil {
		report(stderr, "filees-worker mobile-onboard activation", err)
		return ExitConfig
	}
	if err := manager.Stage(grant); err != nil {
		report(stderr, "filees-worker mobile-onboard stage", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, mobileWorkerResult{Schema: mobileWorkerResultSchema, Status: "staged", OperationID: grant.OperationID, ClientID: grant.ClientID}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

// RunMobileProofWorker is exec'd by RunMobileEntry's filees-mobile-proof/v1
// branch. It only records that the freshly-generated installation key
// proved possession by successfully connecting here - the same
// Manager.RecordProof desktop's ServiceProofCommand already relies on,
// already transport-agnostic, unchanged.
func RunMobileProofWorker(args []string, stdout, stderr io.Writer) int {
	return runMobileProofWorker("/etc/filees/server.json", args, stdout, stderr)
}

func runMobileProofWorker(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		fmt.Fprintln(stderr, "filees-worker mobile-proof: operation_id and client_id required")
		return ExitUsage
	}
	operationID, clientID := args[0], args[1]
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		report(stderr, "filees-worker mobile-proof config", err)
		return ExitConfig
	}
	manager, err := activation.New(config.Activation, nil)
	if err != nil {
		report(stderr, "filees-worker mobile-proof activation", err)
		return ExitConfig
	}
	if err := manager.RecordProof(operationID, clientID); err != nil {
		report(stderr, "filees-worker mobile-proof record", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, mobileWorkerResult{Schema: mobileWorkerResultSchema, Status: "proved", OperationID: operationID, ClientID: clientID}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

// RunMobileFinishWorker is exec'd by RunMobileEntry's
// filees-mobile-finish/v1 branch, after the phone's separate proof
// connection succeeded. It rebuilds the ActivationGrant from the persisted
// Operation (the same fields activationGrantFor would copy - that helper is
// unexported in pkg/onboarding, so this mirrors it directly, exactly like
// internal/servertool/worker.go's own deploy-worker finish step already
// does for desktop) and calls the same HasProof/Publish desktop uses.
func RunMobileFinishWorker(args []string, stdout, stderr io.Writer) int {
	return runMobileFinishWorker("/etc/filees/server.json", args, stdout, stderr)
}

func runMobileFinishWorker(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		fmt.Fprintln(stderr, "filees-worker mobile-finish: operation_id and client_id required")
		return ExitUsage
	}
	operationID, clientID := args[0], args[1]
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation|serverconfig.SecretOTP)
	if err != nil {
		report(stderr, "filees-worker mobile-finish config", err)
		return ExitConfig
	}
	files, err := onboarding.OpenPrepared(config.Root, config.Onboarding, onboarding.Access{Areas: onboarding.AreaOperations | onboarding.AreaAudit, NeedOTP: true})
	if err != nil {
		report(stderr, "filees-worker mobile-finish onboarding", err)
		return ExitConfig
	}
	op, err := files.GetOperation(operationID)
	if err != nil {
		report(stderr, "filees-worker mobile-finish operation", err)
		return ExitTempFail
	}
	if op.ClientID != clientID {
		report(stderr, "filees-worker mobile-finish", errors.New("operation does not belong to this client"))
		return ExitUnavailable
	}
	grant := onboarding.ActivationGrant{
		OperationID: op.OperationID, ClientID: op.ClientID, RealmID: op.ApprovedPolicy.RealmID, Kind: op.ApprovedPolicy.Kind,
		DeployRequestID: op.DeployRequestID, InstallationPublicKey: op.InstallationPublicKey,
		InstallationFingerprint: op.InstallationFingerprint, ExpiresAt: op.ExpiresAt,
	}
	manager, err := activation.New(config.Activation, nil)
	if err != nil {
		report(stderr, "filees-worker mobile-finish activation", err)
		return ExitConfig
	}
	if err := manager.HasProof(grant); err != nil {
		report(stderr, "filees-worker mobile-finish proof", err)
		return ExitTempFail
	}
	revision, err := manager.Publish(context.Background(), grant)
	if err != nil {
		report(stderr, "filees-worker mobile-finish publish", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, mobileWorkerResult{Schema: mobileWorkerResultSchema, Status: "active", OperationID: operationID, ClientID: clientID, ServiceRevision: revision}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}
