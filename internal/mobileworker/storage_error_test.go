package mobileworker

import (
	"bytes"
	"context"
	v1 "filees/pkg/mobile/v1"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestStorageFullWireAndLocalDiagnostic(t *testing.T) {
	for _, cause := range []error{fmt.Errorf("spool: %w", syscall.ENOSPC), fmt.Errorf("quota: %w", syscall.EDQUOT), fmt.Errorf("%w: /private/path", ErrStorageFull)} {
		d := Dispatcher{Appender: Appender{Ledger: Ledger{Dir: t.TempDir()}}}
		req, _ := v1.NewRequest(uuid.NewString(), v1.OpUploadTree, v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: 1, Sha256: strings.Repeat("a", 64)})
		var out bytes.Buffer
		if err := d.writeError(&out, req, cause); err != nil {
			t.Fatal(err)
		}
		h, _, err := v1.ReadFrame(&out, v1.ResponseMagic, v1.MaxHeaderBytes)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := v1.ParseResponse(h)
		if err != nil || resp.Error == nil || resp.Error.Code != "storage.full" || strings.Contains(string(h), "/private/") {
			t.Fatalf("response %s %v", h, err)
		}
		log, err := os.ReadFile(filepath.Join(d.Appender.Ledger.Dir, "errors.log"))
		if err != nil || !bytes.Contains(log, []byte(req.RequestID)) {
			t.Fatalf("log %s %v", log, err)
		}
	}
}
func TestSVNStorageFullBeforeTruncation(t *testing.T) {
	stderr := "svn: E000028: Can't write private/path: No space left on device\n" + strings.Repeat("later diagnostics\n", 90)
	if !svnStorageFull(stderr) {
		t.Fatal("missed numeric error")
	}
	for _, s := range []string{"Adding svn: E000028: file", "No space left on device.txt", "svn: E170013: Unable to connect"} {
		if svnStorageFull(s) {
			t.Fatalf("false positive: %s", s)
		}
	}
	// Exercise the actual subprocess-to-typed-error boundary without a full disk.
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("shell unavailable")
	}
	err := runStream(context.Background(), &bytes.Buffer{}, "/bin/sh", "-c", "printf '%s\\n' 'svn: E000028: private/path' >&2; exit 1")
	if !IsStorageFull(err) {
		t.Fatalf("not classified: %v", err)
	}
}
