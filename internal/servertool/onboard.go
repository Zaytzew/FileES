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
	files, _, err := openFiles(path, toolAccess{name: "filees-onboard/take", areas: onboarding.AreaAll, write: true, needOTP: true})
	if err != nil {
		report(stderr, "filees-onboard config", err)
		return ExitConfig
	}
	_, err = files.Take(request.Email, request.OnboardingRequestID)
	if err != nil && !errors.Is(err, onboarding.ErrTicketUnavailable) {
		report(stderr, "filees-onboard", err)
		return ExitTempFail
	}
	if _, err := stdout.Write(onboarding.EncodeOnboardResponse(request.OnboardingRequestID)); err != nil {
		return ExitSoftware
	}
	return ExitOK
}
