package ipcclient

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// daemonCommandLimits reads the daemon's own limits from its source: which
// handler serves which command (the dispatch switch), and the longest
// context.WithTimeout(context.Background(), N*time.Unit) inside each handler.
func daemonCommandLimits(t *testing.T) map[string]time.Duration {
	t.Helper()
	names := map[string]string{}
	contractSrc, err := os.ReadFile(filepath.Join("..", "contract", "v1", "commands.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*(Cmd\w+)\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(contractSrc), -1) {
		names[m[1]] = m[2]
	}
	files, err := filepath.Glob(filepath.Join("..", "ipcserver", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source.Write(raw)
		source.WriteByte('\n')
	}
	src := source.String()
	handlerLimit := map[string]time.Duration{}
	funcs := regexp.MustCompile(`(?m)^func \(s \*Server\) (handle\w+)\(`).FindAllStringSubmatchIndex(src, -1)
	timeout := regexp.MustCompile(`context\.WithTimeout\(context\.Background\(\), (\d+)\s*\*\s*time\.(Second|Minute|Hour)\)`)
	for i, f := range funcs {
		end := len(src)
		if i+1 < len(funcs) {
			end = funcs[i+1][0]
		}
		name := src[f[2]:f[3]]
		for _, m := range timeout.FindAllStringSubmatch(src[f[0]:end], -1) {
			n, _ := strconv.Atoi(m[1])
			unit := map[string]time.Duration{"Second": time.Second, "Minute": time.Minute, "Hour": time.Hour}[m[2]]
			if d := time.Duration(n) * unit; d > handlerLimit[name] {
				handlerLimit[name] = d
			}
		}
	}
	limits := map[string]time.Duration{}
	dispatch := regexp.MustCompile(`case ((?:contract\.Cmd\w+,?\s*)+):\s*\n\s*return s\.(handle\w+)\(`)
	for _, m := range dispatch.FindAllStringSubmatch(src, -1) {
		limit, ok := handlerLimit[m[2]]
		if !ok {
			continue
		}
		for _, constant := range regexp.MustCompile(`Cmd\w+`).FindAllString(m[1], -1) {
			if command, ok := names[constant]; ok {
				limits[command] = limit
			}
		}
	}
	if len(limits) < 10 {
		t.Fatalf("parsed only %d daemon limits; the source layout changed and this test no longer sees it", len(limits))
	}
	return limits
}

// A daemon that allows itself longer than the IPC default must be waited for
// that long: "read unix ... daemon.sock: i/o timeout" after 10 seconds, while
// the daemon kept working, was the answer to a release published over SSH
// (Windows Sandbox, 2026-09-24).
func TestEveryLongDaemonCommandIsWaitedForAsLongAsItRuns(t *testing.T) {
	for command, limit := range daemonCommandLimits(t) {
		if limit <= defaultTimeout {
			continue
		}
		if got := timeoutFor(command, defaultTimeout); got < limit {
			t.Errorf("%s: the daemon allows %v, the client waits %v", command, limit, got)
		}
	}
}

func TestShortCommandsKeepTheDefault(t *testing.T) {
	if got := timeoutFor("repo.status", defaultTimeout); got != defaultTimeout {
		t.Fatalf("repo.status waits %v; a refresh must stay short", got)
	}
}
