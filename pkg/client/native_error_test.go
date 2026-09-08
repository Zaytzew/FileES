package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"filees/pkg/errcat"
	"filees/pkg/errmap"
)

func receipt(entries ...string) string {
	return `{"schema":"filees.native-svn/v1","ok":false,"errors":[` + strings.Join(entries, ",") + `]}`
}

// The reason the chain is worth carrying at all. M9 records that an identity
// refusal arrives wrapped in E170013 and gets classified as network - a
// permanent cause reported as a transient one, which is how a client retries
// forever against a revoked key. The deeper code is the answer.
func TestDeeperCodeBeatsTheWrapper(t *testing.T) {
	err := nativeFault("status", nil, false,
		receipt(`{"code":170013,"message":"Unable to connect to a repository at URL"}`,
			`{"code":170001,"message":"Authorization failed"}`), "")

	var fault errcat.Fault
	if !errors.As(err, &fault) {
		t.Fatalf("expected a typed fault, got %T: %v", err, err)
	}
	if fault.Key != errcat.KeyAuthFailed {
		t.Fatalf("key = %q, want %q: the wrapper must not decide", fault.Key, errcat.KeyAuthFailed)
	}
	if fault.Details["native_code"] != "E170001" {
		t.Fatalf("native_code = %q, want E170001", fault.Details["native_code"])
	}
}

// With nothing more specific, the wrapper is still better than silence.
func TestWrapperAloneStillClassifies(t *testing.T) {
	err := nativeFault("status", nil, false,
		receipt(`{"code":170013,"message":"Unable to connect to a repository at URL"}`), "")

	var fault errcat.Fault
	if !errors.As(err, &fault) || fault.Key != errcat.KeyNetUnreachable {
		t.Fatalf("expected net.unreachable, got %v", err)
	}
}

// An unrecognised chain must NOT become a confident guess. Returning the raw
// failure leaves errmap's text heuristics their chance; a wrong Fault would
// pre-empt them and stop anybody looking further.
func TestUnknownCodesDoNotFabricateAClassification(t *testing.T) {
	err := nativeFault("status", nil, false,
		receipt(`{"code":999999,"message":"something new"}`), "")

	var fault errcat.Fault
	if errors.As(err, &fault) {
		t.Fatalf("unknown code produced a classification: %+v", fault)
	}
	var failure *NativeFailure
	if !errors.As(err, &failure) || len(failure.Entries) != 1 {
		t.Fatalf("raw codes must survive for diagnostics: %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "E999999") {
		t.Fatalf("message lost the code: %v", err)
	}
}

// The whole point of M46: errmap classifies from the code, not from English
// words that happen to appear in the text.
func TestErrmapTakesTheTypedBranch(t *testing.T) {
	err := nativeFault("status", nil, false,
		receipt(`{"code":155004,"message":"Working copy locked"}`), "")

	entry := errmap.Classify(err)
	if entry.Key != errcat.KeyWorkingCopyBusy {
		t.Fatalf("errmap key = %q, want %q", entry.Key, errcat.KeyWorkingCopyBusy)
	}
}

// A killed process reports only "signal: killed". Losing the deadline would
// turn a retryable timeout into an unexplained failure.
func TestDeadlineSurvivesTheTypedError(t *testing.T) {
	err := nativeFault("status", errors.Join(errors.New("signal: killed"), context.DeadlineExceeded), false, "", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}

// A classification made from half an answer has to say so.
func TestTruncationIsVisible(t *testing.T) {
	err := nativeFault("status", nil, true,
		receipt(`{"code":155004,"message":"Working copy locked"}`), "")
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncation not reported: %v", err)
	}
}

// Diagnostics keep the whole output, not the part we knew how to name.
func TestRawOutputIsKept(t *testing.T) {
	err := nativeFault("status", nil, false, receipt(`{"code":155004,"message":"locked"}`), "stderr line")
	var failure *NativeFailure
	if !errors.As(err, &failure) {
		t.Fatalf("no NativeFailure in chain: %T", err)
	}
	if !strings.Contains(failure.Output, "stderr line") {
		t.Fatalf("stderr dropped: %q", failure.Output)
	}
}
