package servertool

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"
)

func TestWhaleWorkerRejectsPayloadSelectableClientIdentity(t *testing.T) {
	for _, clientID := range []string{"", "../clients/another", "A80A065D-4DCE-4D21-B3D6-EE8A4CDE8CA2"} {
		var stderr bytes.Buffer
		code := runWhaleWorker(filepath.Join(t.TempDir(), "missing-server.json"), []string{clientID}, bytes.NewReader(nil), io.Discard, &stderr)
		if code != ExitUsage {
			t.Fatalf("client ID %q returned %d, want usage; stderr=%s", clientID, code, stderr.String())
		}
	}
}
