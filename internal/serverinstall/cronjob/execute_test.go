//go:build !windows

package cronjob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyRereadsAndRejectsReadFailure(t *testing.T) {
	dir := t.TempDir()
	table := filepath.Join(dir, "table")
	script := filepath.Join(dir, "crontab")
	body := "#!/bin/sh\nif [ \"$3\" = -l ]; then cat '" + table + "'; else cat > '" + table + "'; fi\n"
	if e := os.WriteFile(script, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	line, e := Line("/usr/local/sbin/filees-admin", "/etc/filees/server.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(table, []byte("# concurrent admin change\n"), 0600); e != nil {
		t.Fatal(e)
	}
	p := Plan{Program: script, Line: line, Before: "older", Changed: true}
	if e = p.Apply(context.Background()); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(table)
	if !strings.HasPrefix(string(b), "# concurrent admin change\n") || strings.Count(string(b), Marker) != 1 {
		t.Fatal(string(b))
	}
	if e = p.Apply(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(script, []byte("#!/bin/sh\necho 'permission denied' >&2\nexit 1\n"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = p.Apply(context.Background()); e == nil {
		t.Fatal("read failure treated as empty")
	}
	unchanged, _ := os.ReadFile(table)
	if string(unchanged) != string(b) {
		t.Fatal("table lost after read failure")
	}
}
