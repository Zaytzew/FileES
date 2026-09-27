package cronjob

import (
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
