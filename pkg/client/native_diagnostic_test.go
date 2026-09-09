package client

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativeDiagnosticKeepsExitAndStderrWithKnownCodes(t *testing.T) {
	err := nativeFault("commit", context.DeadlineExceeded, false, `{"schema":"filees.native-svn/v1","ok":false,"errors":[{"code":155004,"message":"writer busy"}]}`, "ssh transport diagnostic\nnext line")
	for _, want := range []string{"E155004", "deadline exceeded", "ssh transport diagnostic", `\nnext line`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatal("missing", want, err)
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline identity lost")
	}
}
