package repoworker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Called only by the Unix generation-swap integration test, after fixture
// creation and absolute tool lookup. No repository or global PATH is changed
// outside this test process and its temporary directory.
func trackConfiguredSVNAdmin(t *testing.T, svc *DumpLoadService) func() string {
	t.Helper()
	root := t.TempDir()
	logPath := filepath.Join(root, "commands")
	selected := filepath.Join(root, "selected-svnadmin")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + quote(logPath) + "\nexec " + quote(svc.SVNAdmin) + " \"$@\"\n"
	if err := os.WriteFile(selected, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	trap := filepath.Join(root, "svnadmin")
	if err := os.WriteFile(trap, []byte("#!/bin/sh\necho 'unexpected svnadmin from PATH' >&2\nexit 91\n"), 0700); err != nil {
		t.Fatal(err)
	}
	svc.SVNAdmin = selected
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		t.Helper()
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}
