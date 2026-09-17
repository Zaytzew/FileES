package servertool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

// demoReapGrace is the cron cadence of `filees-admin demo reap`: a realm's
// space is free one run after its TTL, not at the TTL itself.
const demoReapGrace = time.Minute

func RunOnboard(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	path, args, err := configPath(args)
	if err != nil || len(args) != 1 || args[0] != "take" {
		fmt.Fprintln(stderr, "usage: filees-onboard [-config path] take < request.json")
		return ExitUsage
	}
	request, err := onboarding.DecodeOnboardRequest(stdin)
	if err != nil {
		report(stderr, "filees-onboard request", err)
		return ExitData
	}
	demo := request.Schema == onboarding.DemoOnboardRequestSchema
	files, config, err := openFiles(path, toolAccess{name: "filees-onboard/take", areas: onboarding.AreaAll, write: true, needOTP: true, needWorkerPublic: true, needDemoCapacity: demo})
	if err != nil {
		report(stderr, "filees-onboard config", err)
		return ExitConfig
	}
	if demo {
		return runDemoOnboard(files, config, request, os.Getenv("SSH_CONNECTION"), stdout, stderr)
	}
	var receipt onboarding.TakeReceipt
	if request.Schema == onboarding.LegacyOnboardRequestSchema {
		receipt, err = files.Take(request.Email, request.OnboardingRequestID)
	} else {
		receipt, err = files.TakeInvitation(request.InvitationToken, request.ProposedRealmID, request.OnboardingRequestID)
	}
	if err != nil && !errors.Is(err, onboarding.ErrTicketUnavailable) {
		report(stderr, "filees-onboard", err)
		return ExitTempFail
	}
	port := receipt.AssignedReversePort
	if port == 0 {
		// Keep an unavailable invitation indistinguishable from an accepted
		// one at this unauthenticated boundary. The later OTP is still the
		// only proof and the only operation-capable credential.
		port = config.Onboarding.ReversePortFirst
	}
	if _, err := stdout.Write(onboarding.EncodeOnboardResponse(request.OnboardingRequestID, config.WorkerPublicKey, port)); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

// runDemoOnboard serves the invitation-less request only a demo server knows.
// An ordinary server treats it as malformed, exactly as before it existed.
func runDemoOnboard(files *onboarding.Files, config serverconfig.Config, request onboarding.OnboardRequest, sshConnection string, stdout, stderr io.Writer) int {
	if !config.Demo.Enabled {
		report(stderr, "filees-onboard request", errors.New("this server does not offer demo activation"))
		return ExitData
	}
	clientIP, err := onboarding.DemoClientAddress(sshConnection)
	if err != nil {
		report(stderr, "filees-onboard demo", err)
		return ExitData
	}
	capacity := repoworker.FilesystemCapacity{Root: config.Repositories.Root}
	admission := onboarding.DemoAdmission{
		RealmTTL: config.Demo.RealmTTL, IPBlock: config.Demo.AddressBlock, RealmQuota: config.Demo.RealmQuota, ReapGrace: demoReapGrace,
		FreeBytes: func() (int64, error) {
			available, _, err := capacity.Check(context.Background(), 0)
			return available, err
		},
	}
	receipt, err := files.TakeDemo(request.Email, request.InstallationUID, clientIP, request.OnboardingRequestID, admission)
	var refusal *onboarding.DemoRefusal
	if errors.As(err, &refusal) {
		if _, err := stdout.Write(onboarding.EncodeDemoRefusal(request.OnboardingRequestID, refusal)); err != nil {
			return ExitSoftware
		}
		return ExitOK
	}
	if err != nil {
		report(stderr, "filees-onboard demo", err)
		return ExitTempFail
	}
	if _, err := stdout.Write(onboarding.EncodeOnboardResponse(request.OnboardingRequestID, config.WorkerPublicKey, receipt.AssignedReversePort)); err != nil {
		return ExitSoftware
	}
	return ExitOK
}
