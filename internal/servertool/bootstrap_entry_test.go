package servertool

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestBootstrapEntryRunsOnboardThenOneMailSubmission(t *testing.T) {
	if isolateSandboxingTest(t, "TestBootstrapEntryRunsOnboardThenOneMailSubmission") {
		return
	}
	permitRepeatedSandbox(t)
	if bootstrapEntryPromises != "stdio rpath proc exec" {
		t.Fatalf("bootstrap entry promises = %q", bootstrapEntryPromises)
	}
	if bootstrapChildPromises != "stdio rpath wpath cpath fattr flock inet dns proc unveil" {
		t.Fatalf("bootstrap child promises = %q", bootstrapChildPromises)
	}
	original := runBootstrapChild
	originalPledge := bootstrapPledge
	defer func() { runBootstrapChild, bootstrapPledge = original, originalPledge }()
	bootstrapPledge = func(string, string) error { return nil }
	var calls []string
	runBootstrapChild = func(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		calls = append(calls, path+" "+args[0])
		return nil
	}
	if code := RunBootstrapEntry(nil, bytes.NewBufferString("request"), io.Discard, io.Discard); code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	want := []string{onboardPath + " take", mailPath + " send"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestBootstrapEntryDoesNotSendMailWhenOnboardFails(t *testing.T) {
	if isolateSandboxingTest(t, "TestBootstrapEntryDoesNotSendMailWhenOnboardFails") {
		return
	}
	permitRepeatedSandbox(t)
	original := runBootstrapChild
	originalPledge := bootstrapPledge
	defer func() { runBootstrapChild, bootstrapPledge = original, originalPledge }()
	bootstrapPledge = func(string, string) error { return nil }
	calls := 0
	runBootstrapChild = func(string, []string, io.Reader, io.Writer, io.Writer) error {
		calls++
		return errors.New("failed")
	}
	if code := RunBootstrapEntry(nil, nil, io.Discard, io.Discard); code != ExitTempFail || calls != 1 {
		t.Fatalf("exit=%d calls=%d", code, calls)
	}
}
