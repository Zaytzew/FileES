package servertool

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"filees/pkg/onboarding"
)

type bsdAuthInvocation struct {
	service, username, class string
}

func RunSSHAuth(args []string, auth io.ReadWriter, stderr io.Writer) int {
	invocation, err := parseBSDAuthInvocation(args)
	if err != nil {
		bsdReject(auth)
		return ExitUsage
	}
	if invocation.username != "_filees-tunnel" || invocation.class != "filees-tunnel" {
		bsdReject(auth)
		return ExitUnavailable
	}
	switch invocation.service {
	case "challenge":
		// The fixed challenge discloses neither an operation locator nor any
		// server state. An explicit challenge is used because sshd keeps the BSD
		// Auth session only after "reject challenge".
		_, _ = io.WriteString(auth, "value challenge FileES OTP: \\n\nreject challenge\n")
		return ExitOK
	case "response":
		challenge, otp, err := readBSDResponse(auth)
		if err != nil || challenge != "FileES OTP: \n" {
			bsdReject(auth)
			return ExitOK
		}
		files, _, err := openFiles("/etc/filees/server.json", toolAccess{name: "filees-ssh-auth/response", areas: onboarding.AreaOperations, write: true, needOTP: true})
		if err != nil {
			fmt.Fprintln(stderr, "filees SSH authentication unavailable")
			bsdReject(auth)
			return ExitTempFail
		}
		_, err = files.AuthenticateOTP(otp)
		if err != nil {
			bsdReject(auth)
			return ExitOK
		}
		_, _ = io.WriteString(auth, "authorize\n")
		return ExitOK
	default:
		bsdReject(auth)
		return ExitUsage
	}
}

func parseBSDAuthInvocation(args []string) (bsdAuthInvocation, error) {
	result := bsdAuthInvocation{service: "login"}
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		if len(args) < 2 {
			return result, errors.New("incomplete BSD Auth option")
		}
		switch args[0] {
		case "-s":
			result.service = args[1]
		case "-v":
			if !strings.Contains(args[1], "=") {
				return result, errors.New("invalid BSD Auth variable")
			}
		default:
			return result, errors.New("unknown BSD Auth option")
		}
		args = args[2:]
	}
	if len(args) != 2 {
		return result, errors.New("BSD Auth requires username and class")
	}
	result.username, result.class = args[0], args[1]
	return result, nil
}

func readBSDResponse(reader io.Reader) (string, string, error) {
	buffered := bufio.NewReader(io.LimitReader(reader, 2048))
	read := func() (string, error) {
		raw, err := buffered.ReadBytes(0)
		if err != nil || len(raw) == 0 || len(raw) > 1025 {
			return "", errors.New("invalid BSD Auth response")
		}
		return string(raw[:len(raw)-1]), nil
	}
	challenge, err := read()
	if err != nil {
		return "", "", err
	}
	response, err := read()
	if err != nil || buffered.Buffered() != 0 {
		return "", "", errors.New("invalid BSD Auth response")
	}
	return challenge, response, nil
}

func bsdReject(writer io.Writer) { _, _ = io.WriteString(writer, "reject\n") }
