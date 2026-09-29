package svnrotate

import (
	"io"
	"strings"
	"testing"
)

func TestDiagnosticTailBoundsAndKeepsLastBytes(t *testing.T) {
	var b diagnosticTail
	if _, err := io.Copy(&b, strings.NewReader(strings.Repeat("x", 2<<20))); err != nil {
		t.Fatal(err)
	}
	_, _ = b.Write([]byte("last diagnostic"))
	if len(b.Bytes()) != 4096 || !strings.HasSuffix(string(b.Bytes()), "last diagnostic") {
		t.Fatal("tail not bounded or lost last diagnostic")
	}
}
