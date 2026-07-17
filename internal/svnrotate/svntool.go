package svnrotate

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runTool executes an SVN tool with stderr captured; a failure error carries
// the stderr tail so the operator sees the actual SVN diagnostic.
func runTool(stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, stderrTail(errb.Bytes()))
	}
	return nil
}

func outputTool(name string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	if err := runTool(nil, &out, name, args...); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func stderrTail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "(no stderr)"
	}
	const max = 512
	if len(s) > max {
		s = "…" + s[len(s)-max:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

// fileURL builds a correctly encoded file:// URL from an absolute path.
func fileURL(absPath string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}
	return u.String()
}

func headRev(repoPath string) (int, error) {
	out, err := outputTool("svnlook", "youngest", repoPath)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("svnlook youngest: unexpected output %q", strings.TrimSpace(string(out)))
	}
	return n, nil
}

// revDate returns the svn:date of the given revision.
func revDate(repoPath string, rev int) (time.Time, error) {
	out, err := outputTool("svnlook", "date", "-r", strconv.Itoa(rev), repoPath)
	if err != nil {
		return time.Time{}, err
	}
	return parseSvnlookDate(string(out))
}

// parseSvnlookDate parses svnlook date output, e.g.
// "2026-07-17 14:22:33 +0200 (Fri, 17 Jul 2026)". The parenthesised part is
// locale-dependent and ignored.
func parseSvnlookDate(s string) (time.Time, error) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) < 3 {
		return time.Time{}, fmt.Errorf("svnlook date: unexpected output %q", strings.TrimSpace(s))
	}
	t, err := time.Parse("2006-01-02 15:04:05 -0700", strings.Join(fields[:3], " "))
	if err != nil {
		return time.Time{}, fmt.Errorf("svnlook date: %w", err)
	}
	return t, nil
}

func pack(repoPath string) error {
	return runTool(nil, io.Discard, "svnadmin", "pack", repoPath)
}

func verify(repoPath string) error {
	return runTool(nil, io.Discard, "svnadmin", "verify", "--quiet", repoPath)
}

func svnadminCreate(repoPath string) error {
	return runTool(nil, io.Discard, "svnadmin", "create", repoPath)
}

// activeLocks returns the raw svnadmin lslocks output; empty means no locks.
func activeLocks(repoPath string) (string, error) {
	out, err := outputTool("svnadmin", "lslocks", repoPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// treeListing returns svnlook tree --full-paths output for rev, used to
// compare old HEAD against the new generation's r1 before switching.
func treeListing(repoPath string, rev int) (string, error) {
	out, err := outputTool("svnlook", "tree", "--full-paths", "-r", strconv.Itoa(rev), repoPath)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// dumpRevLoad pipes `svnadmin dump -r rev` (non-incremental: the first dumped
// revision is a complete tree with all versioned properties) into
// `svnadmin load` of dstRepo, becoming its r1. The connection is a kernel
// pipe between the two child processes — no in-process copying, so an early
// exit of either side surfaces as EPIPE/EOF in the other instead of a
// deadlocked goroutine.
func dumpRevLoad(srcRepo, dstRepo string, rev int) error {
	dump := exec.Command("svnadmin", "dump", "--quiet", "-r", strconv.Itoa(rev), srcRepo)
	// --ignore-uuid is load-bearing for the V2 model: a dump stream carries
	// the source repo's UUID, and loading into an empty repo would silently
	// adopt it — resurrecting exactly the fake-continuity the review
	// rejected. The new generation keeps its own fresh UUID.
	load := exec.Command("svnadmin", "load", "--quiet", "--ignore-uuid", dstRepo)

	var dumpErrb, loadErrb bytes.Buffer
	dump.Stderr = &dumpErrb
	load.Stderr = &loadErrb

	pipe, err := dump.StdoutPipe()
	if err != nil {
		return err
	}
	load.Stdin = pipe
	load.Stdout = io.Discard

	if err := dump.Start(); err != nil {
		return fmt.Errorf("svnadmin dump: %w", err)
	}
	if err := load.Start(); err != nil {
		_ = dump.Process.Kill()
		_ = dump.Wait()
		return fmt.Errorf("svnadmin load: %w", err)
	}
	dumpErr := dump.Wait() // closes the pipe's write end
	loadErr := load.Wait()

	if loadErr != nil {
		return fmt.Errorf("svnadmin load: %w: %s", loadErr, stderrTail(loadErrb.Bytes()))
	}
	if dumpErr != nil {
		return fmt.Errorf("svnadmin dump: %w: %s", dumpErr, stderrTail(dumpErrb.Bytes()))
	}
	return nil
}

// writeLogXML writes `svn log --xml -v` for the full history to w.
func writeLogXML(repoPath string, w io.Writer) error {
	return runTool(nil, w, "svn", "log", "--xml", "-v",
		"-r", "1:HEAD", fileURL(repoPath), "--non-interactive", "--no-auth-cache")
}

// writeRangeDump streams a dump of revisions low:high to w. Non-incremental,
// so the first dumped revision is a complete tree and the artifact is
// independently restorable with svnadmin load.
func writeRangeDump(repoPath string, w io.Writer, low, high int) error {
	return runTool(nil, w, "svnadmin", "dump", "--quiet",
		"-r", fmt.Sprintf("%d:%d", low, high), repoPath)
}
