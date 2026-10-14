//go:build windows

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeCommandCanInspectLongWorkingCopyWithoutLongProcessCWD(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true}`)
	dir := filepath.Join(t.TempDir(), strings.Repeat("folder", 16), strings.Repeat("deep", 20), strings.Repeat("long", 20))
	if len(dir) < 270 {
		t.Fatal("fixture too short")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	if _, err := c.nativeCommand(t.Context(), dir, 10*time.Second, "info", "--inspect-wc", dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(raw), strings.ReplaceAll(dir, `\`, `\\`)) {
		t.Fatalf("absolute WC argument changed: %s %v", raw, err)
	}
}
