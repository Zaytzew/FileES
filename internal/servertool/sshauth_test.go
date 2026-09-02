package servertool

import (
	"bytes"
	"strings"
	"testing"

	"filees/pkg/onboarding"
)

type authBuffer struct{ bytes.Buffer }

func TestBSDAuthChallengeIsEnumerationFree(t *testing.T) {
	if isolateSandboxingTest(t, "TestBSDAuthChallengeIsEnumerationFree") {
		return
	}
	auth := &authBuffer{}
	if code := RunSSHAuth([]string{"-v", "auth_type=auth-ssh", "-s", "challenge", "--", "_filees-tunnel", "filees-tunnel"}, auth, &bytes.Buffer{}); code != ExitOK {
		t.Fatalf("challenge exit=%d", code)
	}
	got := auth.String()
	if !strings.HasPrefix(got, "value challenge "+onboarding.ReconnectChallengePrefix) || !strings.HasSuffix(got, ":\\n\nreject challenge\n") {
		t.Fatalf("challenge protocol=%q", got)
	}
}

func TestBSDAuthRejectsWrongPrincipalAndMalformedResponse(t *testing.T) {
	if isolateSandboxingTest(t, "TestBSDAuthRejectsWrongPrincipalAndMalformedResponse") {
		return
	}
	for _, test := range []struct {
		args []string
		wire string
	}{
		{[]string{"-s", "response", "someone", "filees-tunnel"}, "\x00otp\x00"},
		{[]string{"-s", "response", "_filees-tunnel", "filees-tunnel"}, "nonempty\x00otp\x00"},
		{[]string{"-s", "response", "_filees-tunnel", "filees-tunnel"}, "missing-nuls"},
		{[]string{"-x", "value", "_filees-tunnel", "filees-tunnel"}, ""},
	} {
		auth := &authBuffer{}
		auth.WriteString(test.wire)
		_ = RunSSHAuth(test.args, auth, &bytes.Buffer{})
		if !strings.HasSuffix(auth.String(), "reject\n") {
			t.Fatalf("args=%v response did not reject: %q", test.args, auth.String())
		}
	}
}

func TestBSDResponseParserIsBoundedAndExact(t *testing.T) {
	challenge, err := onboarding.NewReconnectChallenge(strings.NewReader(strings.Repeat("n", 32)))
	if err != nil {
		t.Fatal(err)
	}
	responseChallenge, response, err := readBSDResponse(strings.NewReader(challenge + "\x00AAAAAAAA-BBBBBBBBBBBBBBBB\x00"))
	if err != nil || responseChallenge != challenge || response != "AAAAAAAA-BBBBBBBBBBBBBBBB" {
		t.Fatalf("challenge=%q response=%q err=%v", challenge, response, err)
	}
	for _, wire := range []string{"\x00otp", "\x00otp\x00junk", strings.Repeat("a", 1026) + "\x00x\x00"} {
		if _, _, err := readBSDResponse(strings.NewReader(wire)); err == nil {
			t.Fatalf("accepted malformed response of length %d", len(wire))
		}
	}
}

func TestEntryRejectsAllNonProtocolCommandsBeforeFilesystemAccess(t *testing.T) {
	if isolateSandboxingTest(t, "TestEntryRejectsAllNonProtocolCommandsBeforeFilesystemAccess") {
		return
	}
	tests := []string{"", "uname -a", "scp -t /tmp/x", "/usr/libexec/sftp-server", "filees tunnel-v1 extra"}
	for _, command := range tests {
		env := func(name string) string {
			if name == "SSH_ORIGINAL_COMMAND" {
				return command
			}
			return ""
		}
		var stderr bytes.Buffer
		if code := RunEntry(nil, strings.NewReader(""), &bytes.Buffer{}, &stderr, env); code != ExitUnavailable {
			t.Fatalf("command %q exit=%d", command, code)
		}
	}
	if code := RunEntry([]string{"-c", TunnelCommand}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, func(string) string { return "" }); code != ExitUnavailable {
		t.Fatalf("entry accepted shell arguments: exit=%d", code)
	}
}
