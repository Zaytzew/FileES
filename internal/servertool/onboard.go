package servertool

import (
	"errors"
	"fmt"
	"io"

	"filees/pkg/onboarding"
)

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
	files, config, err := openFiles(path, toolAccess{name: "filees-onboard/take", areas: onboarding.AreaAll, write: true, needOTP: true, needWorkerPublic: true})
	if err != nil {
		report(stderr, "filees-onboard config", err)
		return ExitConfig
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
