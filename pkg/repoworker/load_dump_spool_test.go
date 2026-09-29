package repoworker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filees/internal/svnrotate"

	"github.com/google/uuid"
)

// A file whose text contains a line "Revision-number: 99" must not move the
// recorded range: only a record header can report a revision.
func TestRevisionRangeSkipsRecordBodies(t *testing.T) {
	text := "\nRevision-number: 99\n\n"
	var d bytes.Buffer
	d.WriteString("SVN-fs-dump-format-version: 2\n\nUUID: 1f0e0c8a-0000-4000-8000-000000000000\n\n")
	d.WriteString("Revision-number: 0\nProp-content-length: 10\nContent-length: 10\n\nPROPS-END\n\n")
	d.WriteString("Revision-number: 1\nProp-content-length: 10\nContent-length: 10\n\nPROPS-END\n\n")
	fmt.Fprintf(&d, "Node-path: a.txt\nNode-kind: file\nNode-action: add\nProp-content-length: 10\nText-content-length: %d\nContent-length: %d\n\nPROPS-END\n%s\n\n", len(text), 10+len(text), text)
	low, high, err := revisionRange(bytes.NewReader(d.Bytes()))
	if err != nil || low != 1 || high != 1 {
		t.Fatalf("revisionRange = (%d,%d,%v), want (1,1,nil)", low, high, err)
	}
}

func TestRevisionRangeRejectsTruncatedAndMalformedStreams(t *testing.T) {
	for name, stream := range map[string]string{
		"truncated body":   "SVN-fs-dump-format-version: 2\n\nRevision-number: 1\nContent-length: 100\n\nPROPS-END\n",
		"truncated header": "SVN-fs-dump-format-version: 2\n\nRevision-number: 1",
		"malformed header": "SVN-fs-dump-format-version: 2\n\nnot a header\n\n",
		"bad length":       "SVN-fs-dump-format-version: 2\n\nRevision-number: 1\nContent-length: -5\n\n",
	} {
		if _, _, err := revisionRange(strings.NewReader(stream)); err == nil {
			t.Fatalf("%s: stream accepted", name)
		}
	}
}

// A real dump whose file content imitates record headers still reports the
// range svnadmin itself wrote.
func TestRevisionRangeOnRealDumpWithHeaderLookalikes(t *testing.T) {
	requireLoadDumpTools(t)
	dump := realDumpBytes(t, t.TempDir(), map[string]string{"a.txt": "\nRevision-number: 99\n\nContent-length: 7\n\n"})
	low, high, err := revisionRange(bytes.NewReader(dump))
	if err != nil || low != 1 || high != 1 {
		t.Fatalf("revisionRange = (%d,%d,%v), want (1,1,nil)", low, high, err)
	}
}

func loadDumpFixture(t *testing.T) (svc DumpLoadService, realm, repoID, repoPath string) {
	t.Helper()
	if !svnrotate.Supported() {
		t.Skip("filees-rotate is only supported on unix systems")
	}
	requireLoadDumpTools(t)
	root := t.TempDir()
	serviceWC := filepath.Join(root, "service")
	realm, repoID = uuid.NewString(), uuid.NewString()
	dump := realDumpBytes(t, root, map[string]string{"docs/a.txt": "hello\n", "docs/b.tmp": "junk\n"})
	reposRoot := buildCarrierRepo(t, root, serviceWC, repoID, realm, dump, "carrier.dump")
	return testDumpLoadService(root, serviceWC, reposRoot), realm, repoID, filepath.Join(reposRoot, repoID)
}

func requireCarrierUntouched(t *testing.T, svc DumpLoadService, repoPath string) {
	t.Helper()
	head, err := exec.Command("svnlook", "youngest", repoPath).CombinedOutput()
	if err != nil || strings.TrimSpace(string(head)) != "1" {
		t.Fatalf("carrier repository changed: head=%s err=%v", head, err)
	}
	entries, err := os.ReadDir(svc.SpoolRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("spool left behind: %v", entries)
	}
}

func TestDumpLoadServiceRefusesCarrierAboveMaxDumpSize(t *testing.T) {
	svc, realm, repoID, repoPath := loadDumpFixture(t)
	svc.MaxDumpBytes = 16
	_, err := svc.Load(context.Background(), realm, repoID, uuid.NewString(), false, nil)
	if err == nil || !strings.Contains(err.Error(), "max_dump_size") {
		t.Fatalf("Load = %v, want max_dump_size refusal", err)
	}
	requireCarrierUntouched(t, svc, repoPath)
}

func TestDumpLoadServiceRefusesBeforeFillingSpool(t *testing.T) {
	svc, realm, repoID, repoPath := loadDumpFixture(t)
	svc.available = func(context.Context, string) (int64, error) { return 1 << 20, nil }
	_, err := svc.Load(context.Background(), realm, repoID, uuid.NewString(), true, nil)
	if err == nil || !strings.Contains(err.Error(), "load_spool_root") {
		t.Fatalf("Load = %v, want spool capacity refusal", err)
	}
	requireCarrierUntouched(t, svc, repoPath)
}

func TestDumpLoadServiceStreamsThroughSpoolAndCleansIt(t *testing.T) {
	svc, realm, repoID, repoPath := loadDumpFixture(t)
	var asked []string
	svc.available = func(_ context.Context, root string) (int64, error) {
		asked = append(asked, root)
		return 1 << 40, nil
	}
	keep := 1
	loaded, err := svc.Load(context.Background(), realm, repoID, uuid.NewString(), true, &keep)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.SourceRevisionRange != "r1:r1" || loaded.NewUUID == "" {
		t.Fatalf("loaded = %+v", loaded)
	}
	if len(asked) == 0 || asked[0] != svc.SpoolRoot {
		t.Fatalf("capacity asked for %v, want the spool first", asked)
	}
	entries, err := os.ReadDir(svc.SpoolRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spool after load = %v, %v; want empty", entries, err)
	}
	tree, err := exec.Command("svnlook", "tree", "--full-paths", repoPath).CombinedOutput()
	if err != nil || strings.Contains(string(tree), "b.tmp") || !strings.Contains(string(tree), "a.txt") {
		t.Fatalf("loaded tree = %s, %v", tree, err)
	}
}

// When the spool shares the repositories' filesystem both needs come out of
// the same free space, so one check must cover their sum.
func TestDumpLoadCapacityAddsNeedsOnSharedVolume(t *testing.T) {
	root := t.TempDir()
	svc := DumpLoadService{SpoolRoot: filepath.Join(root, "spool"), RepositoriesRoot: filepath.Join(root, "repos")}
	for _, dir := range []string{svc.SpoolRoot, svc.RepositoriesRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const carrier = int64(1 << 30)
	margin := carrier / 10
	for _, tc := range []struct {
		filter, bound bool
		want          int64
	}{
		{false, false, carrier + carrier + margin},
		{true, false, 2*carrier + carrier + margin},
		{true, true, 3*carrier + carrier + margin},
	} {
		checks := 0
		svc.available = func(context.Context, string) (int64, error) {
			checks++
			return tc.want, nil
		}
		if err := svc.checkCapacity(context.Background(), carrier, tc.filter, tc.bound); err != nil {
			t.Fatalf("filter=%v bound=%v: exact free space refused: %v", tc.filter, tc.bound, err)
		}
		if checks != 1 {
			t.Fatalf("filter=%v bound=%v: %d checks on one volume, want 1", tc.filter, tc.bound, checks)
		}
		svc.available = func(context.Context, string) (int64, error) { return tc.want - 1, nil }
		if err := svc.checkCapacity(context.Background(), carrier, tc.filter, tc.bound); err == nil {
			t.Fatalf("filter=%v bound=%v: one byte short accepted", tc.filter, tc.bound)
		}
	}
}

func TestOperationSpoolRejectsPathLikeIDs(t *testing.T) {
	svc := DumpLoadService{SpoolRoot: t.TempDir()}
	for _, id := range []string{"", ".", "..", "../x", "a/b", `a\b`} {
		if _, err := svc.operationSpool(id); err == nil {
			t.Fatalf("operation id %q accepted", id)
		}
	}
	stale := filepath.Join(svc.SpoolRoot, "op", "carrier.dump")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := svc.operationSpool("op")
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("retry reused stale spool content: %v", entries)
	}
	if raw, err := os.ReadFile(stale); err != nil || string(raw) != "old" {
		t.Fatalf("retry touched another attempt: %q %v", raw, err)
	}
	other, err := svc.operationSpool("op")
	if err != nil || dir == other {
		t.Fatalf("attempts share a directory: %q %q %v", dir, other, err)
	}
}

func TestRevisionRangeHeaderLimitsAndLengthConsistency(t *testing.T) {
	for name, block := range map[string]string{
		"duplicate":       "Revision-number: 1\nContent-length: 0\nContent-length: 2\n\n",
		"mismatch":        "Revision-number: 1\nProp-content-length: 9\nContent-length: 0\n\n",
		"hidden negative": "Revision-number: 1\nText-content-length: -1\nContent-length: 0\n\n",
		"overflow":        "Revision-number: 1\nProp-content-length: 9223372036854775807\nText-content-length: 1\n\n",
		"mixed node":      "Revision-number: 1\nNode-path: x\n\n",
		"line too big":    "Node-path: " + strings.Repeat("x", maxDumpHeaderLine) + "\n\n",
		"block too big":   "Node-path: " + strings.Repeat("x", maxDumpHeaderLine-30) + "\nA: " + strings.Repeat("x", maxDumpHeaderLine-30) + "\nB: " + strings.Repeat("x", 100) + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := revisionRange(strings.NewReader("SVN-fs-dump-format-version: 2\n\n" + block)); err == nil {
				t.Fatal("malformed dump accepted")
			}
		})
	}
	var many strings.Builder
	for i := 0; i < 65; i++ {
		fmt.Fprintf(&many, "X%d: 0\n", i)
	}
	many.WriteString("\n")
	if _, _, err := revisionRange(strings.NewReader(many.String())); err == nil {
		t.Fatal("unbounded header count")
	}
}

type dumpZeroReader struct{}

func (dumpZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestRevisionRangeLargeBodyHasBoundedAllocation(t *testing.T) {
	const size = 128 << 20
	header := fmt.Sprintf("SVN-fs-dump-format-version: 3\n\nRevision-number: 1\n\nNode-path: x\nText-content-length: %d\nContent-length: %d\n\n", size, size)
	stream := io.MultiReader(strings.NewReader(header), io.LimitReader(dumpZeroReader{}, size), strings.NewReader("\n\nRevision-number: 2\n\n"))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	low, high, err := revisionRange(stream)
	runtime.ReadMemStats(&after)
	if err != nil || low != 1 || high != 2 {
		t.Fatalf("range=%d:%d error=%v", low, high, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("allocated %d bytes to skip 128 MiB", allocated)
	}
}

func TestExtractCarrierAcceptsDashPrefixedName(t *testing.T) {
	requireLoadDumpTools(t)
	root := t.TempDir()
	repoID, realm := uuid.NewString(), uuid.NewString()
	dump := realDumpBytes(t, root, map[string]string{"a.txt": "hello"})
	repos := buildCarrierRepo(t, root, filepath.Join(root, "service"), repoID, realm, dump, "-t")
	svc := testDumpLoadService(root, filepath.Join(root, "service"), repos)
	repo := filepath.Join(repos, repoID)
	name, err := svc.carrierName(context.Background(), repo)
	if err != nil || name != "-t" {
		t.Fatalf("name=%q %v", name, err)
	}
	size, err := svc.carrierSize(context.Background(), repo, name)
	if err != nil || size != int64(len(dump)) {
		t.Fatalf("size=%d %v", size, err)
	}
	dst := filepath.Join(root, "extracted.dump")
	if err := svc.extractCarrier(context.Background(), repo, name, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, dump) {
		t.Fatalf("extract mismatch: %v", err)
	}
}

func TestDumpToolOutputLimits(t *testing.T) {
	if mode := os.Getenv("FILEES_DUMP_TOOL_TEST"); mode != "" {
		if mode == "stderr" {
			fmt.Fprint(os.Stderr, strings.Repeat("x", 2<<20))
			os.Exit(1)
		}
		fmt.Print(strings.Repeat("x", 2<<20))
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FILEES_DUMP_TOOL_TEST", "stdout")
	if _, err := dumpSmallOutput(context.Background(), exe, "-test.run=^TestDumpToolOutputLimits$"); err == nil {
		t.Fatal("truncated metadata accepted")
	}
	t.Setenv("FILEES_DUMP_TOOL_TEST", "stderr")
	err = runDumpTool(context.Background(), nil, io.Discard, exe, "-test.run=^TestDumpToolOutputLimits$")
	if err == nil || len(err.Error()) > 70<<10 || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("diagnostic not bounded: %v", err)
	}
	t.Setenv("FILEES_DUMP_TOOL_TEST", "")
	var output dumpOutput
	if _, err := io.Copy(&output, io.LimitReader(dumpZeroReader{}, 2<<20)); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 64<<10 || !output.truncated {
		t.Fatalf("io.Copy bypassed cap: %d", output.Len())
	}
}
