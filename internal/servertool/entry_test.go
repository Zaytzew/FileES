package servertool

import (
	"bytes"
	"errors"
	"testing"
)

func TestRunEntryDispatchesTunnelAndMobileOnboardSeparately(t *testing.T) {
	tunnelCalls, mobileCalls := 0, 0
	tunnel := func() error { tunnelCalls++; return nil }
	mobile := func() error { mobileCalls++; return nil }
	getenv := func(command string) func(string) string {
		return func(key string) string {
			if key == "SSH_ORIGINAL_COMMAND" {
				return command
			}
			return ""
		}
	}

	var stderr bytes.Buffer
	if code := runEntry(nil, &stderr, getenv(TunnelCommand), tunnel, mobile); code != ExitOK || tunnelCalls != 1 || mobileCalls != 0 {
		t.Fatalf("tunnel dispatch: code=%d tunnel=%d mobile=%d stderr=%s", code, tunnelCalls, mobileCalls, stderr.String())
	}
	if code := runEntry(nil, &stderr, getenv(MobileOnboardCommand), tunnel, mobile); code != ExitOK || tunnelCalls != 1 || mobileCalls != 1 {
		t.Fatalf("mobile onboard dispatch: code=%d tunnel=%d mobile=%d stderr=%s", code, tunnelCalls, mobileCalls, stderr.String())
	}
}

func TestRunEntryRejectsUnknownCommandAndArgs(t *testing.T) {
	never := func() error { t.Fatal("execute called for a rejected command"); return nil }
	getenv := func(key string) string {
		if key == "SSH_ORIGINAL_COMMAND" {
			return "something else"
		}
		return ""
	}
	var stderr bytes.Buffer
	if code := runEntry(nil, &stderr, getenv, never, never); code != ExitUnavailable {
		t.Fatalf("unknown command: code=%d", code)
	}
	tunnelGetenv := func(key string) string {
		if key == "SSH_ORIGINAL_COMMAND" {
			return TunnelCommand
		}
		return ""
	}
	if code := runEntry([]string{"unexpected"}, &stderr, tunnelGetenv, never, never); code != ExitUnavailable {
		t.Fatalf("unexpected args: code=%d", code)
	}
}

func TestRunEntryReportsExecFailure(t *testing.T) {
	failing := func() error { return errors.New("exec failed") }
	getenv := func(key string) string {
		if key == "SSH_ORIGINAL_COMMAND" {
			return MobileOnboardCommand
		}
		return ""
	}
	var stderr bytes.Buffer
	if code := runEntry(nil, &stderr, getenv, failing, failing); code != ExitSoftware {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}
