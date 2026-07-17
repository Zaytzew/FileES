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
		if err := sandboxBegin("stdio"); err != nil {
			bsdReject(auth)
			return ExitTempFail
		}
		// A fresh stateless nonce serves both the initial OTP exchange and a
		// signed reconnect. It contains no operation locator or server state.
		challenge, err := onboarding.NewReconnectChallenge(nil)
		if err != nil {
			bsdReject(auth)
			return ExitTempFail
		}
		// BSD Auth turns the protocol escape "\\n" back into byte 0x0A before
		// echoing the challenge in the response exchange.
		_, _ = fmt.Fprintf(auth, "value challenge %s\\n\nreject challenge\n", strings.TrimSuffix(challenge, "\n"))
		return ExitOK
	case "response":
		challenge, credential, err := readBSDResponse(auth)
		if err != nil {
			bsdReject(auth)
			return ExitOK
		}
		if _, err := onboarding.ParseReconnectChallenge(challenge); err != nil {
			bsdReject(auth)
			return ExitOK
		}
		reconnect := strings.HasPrefix(credential, "FILEES-R1.")
		files, _, err := openFiles("/etc/filees/server.json", toolAccess{name: "filees-ssh-auth/response", areas: onboarding.AreaOperations, write: true, needOTP: !reconnect})
		if err != nil {
			fmt.Fprintln(stderr, "filees SSH authentication unavailable")
			bsdReject(auth)
			return ExitTempFail
		}
		if reconnect {
			_, err = files.AuthenticateReconnect(challenge, credential)
		} else {
			_, err = files.AuthenticateOTP(credential)
		}
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
