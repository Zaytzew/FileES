package cronjob

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPreserveJobsRepairDuplicatesAndQuotePaths(t *testing.T) {
	line, e := Line("/srv/File ES/bin/filees-admin", "/etc/filees/server.json")
	if e != nil {
		t.Fatal(e)
	}
	before := "MAILTO=root\n# local job\n15 3 * * * /bin/backup\n* * * * * old " + Marker + "\n* * * * * duplicate " + Marker + "\n"
	after := Merge(before, line)
	if !strings.HasPrefix(after, "MAILTO=root\n# local job\n15 3 * * * /bin/backup\n") || strings.Count(after, Marker) != 1 || !strings.Contains(after, "'/srv/File ES/bin/filees-admin'") {
		t.Fatal(after)
	}
	if Merge(after, line) != after {
		t.Fatal("not idempotent")
	}
	for _, bad := range []string{"/tmp/a%date", "/tmp/a\ncommand", "relative"} {
		if _, e = Line(bad, "/etc/server.json"); e == nil {
			t.Fatal("accepted unsafe cron path", bad)
		}
	}
}

// With FILEES_SCHEDULER=systemd the Linux installer installs a timer for the
// capacity job. The updater must then neither demand crontab nor add a cron
// entry that would run the job a second time.
func TestCapacityCronStandsDownWhenASystemdTimerRunsTheJob(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd timers are the Linux installer's alternative to cron")
	}
	original := systemdCapacityTimer
	t.Cleanup(func() { systemdCapacityTimer = original })
	systemdCapacityTimer = filepath.Join(t.TempDir(), "filees-capacity.timer")
	if scheduledBySystemd() {
		t.Fatal("a missing unit counted as a scheduler")
	}
	if err := os.WriteFile(systemdCapacityTimer, []byte("[Timer]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(t.Context(), "/usr/local/sbin/filees-admin", "/etc/filees/server.json", false)
	if err != nil || plan != nil {
		t.Fatalf("plan=%v err=%v; the timer already schedules the job", plan, err)
	}
	if err := Ensure(t.Context(), "/usr/local/sbin/filees-admin", "/etc/filees/server.json"); err != nil {
		t.Fatalf("Ensure failed although nothing is left to do: %v", err)
	}
}
