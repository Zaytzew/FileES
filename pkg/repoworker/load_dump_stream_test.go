package repoworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"filees/internal/svnrotate"
	"filees/internal/svnurl"

	"github.com/google/uuid"
)

func TestDumpStreamWriterBound(t *testing.T) {
	for _, tc := range []struct {
		limit       int64
		input, want string
		fails       bool
	}{
		{3, "abc", "abc", false},
		{3, "abcd", "abc", true},
		{0, "x", "", true},
		{0, "", "", false},
		{-1, "abcd", "abcd", false},
	} {
		t.Run(fmt.Sprintf("%d/%d", tc.limit, len(tc.input)), func(t *testing.T) {
			var dst bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &dumpStreamWriter{dst: &dst, remaining: tc.limit, cancel: cancel}
			_, err := io.Copy(w, strings.NewReader(tc.input))
			if errors.Is(err, errDumpStreamLimit) != tc.fails || dst.String() != tc.want || (ctx.Err() != nil) != tc.fails {
				t.Fatalf("output=%q err=%v canceled=%v", dst.String(), err, ctx.Err())
			}
			if tc.fails {
				if n, err := w.Write([]byte("more")); n != 0 || !errors.Is(err, errDumpStreamLimit) || dst.String() != tc.want {
					t.Fatalf("write after refusal: %d %v %q", n, err, dst.String())
				}
			}
		})
	}
}

type failedDumpDestination struct{ err error }

func (w failedDumpDestination) Write([]byte) (int, error) { return 0, w.err }

func TestDumpStreamWriterPreservesDiskError(t *testing.T) {
	for _, diskErr := range []error{errors.New("disk full"), nil} {
		ctx, cancel := context.WithCancel(context.Background())
		w := &dumpStreamWriter{dst: failedDumpDestination{diskErr}, remaining: 10, cancel: cancel}
		_, err := w.Write([]byte("data"))
		want := diskErr
		if want == nil {
			want = io.ErrShortWrite
		}
		if !errors.Is(err, want) || ctx.Err() == nil {
			t.Fatalf("error=%v canceled=%v", err, ctx.Err())
		}
		cancel()
	}
}

// Run the actual output pipe. The over-limit child deliberately stays alive
// after writing, so a broken pipe alone is not sufficient to pass the test.
func TestDumpStreamProcessBound(t *testing.T) {
	if mode := os.Getenv("FILEES_DUMP_STREAM_TEST"); mode != "" {
		n, _ := strconv.Atoi(mode)
		fmt.Print(strings.Repeat("x", n))
		if os.Getenv("FILEES_DUMP_STREAM_WAIT") == "yes" {
			time.Sleep(time.Minute)
		}
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{64 << 10, (64 << 10) + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Setenv("FILEES_DUMP_STREAM_TEST", strconv.Itoa(size))
			if size > 64<<10 {
				t.Setenv("FILEES_DUMP_STREAM_WAIT", "yes")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			dst := filepath.Join(t.TempDir(), "output")
			err := runToFile(ctx, dst, nil, 64<<10, exe, "-test.run=^TestDumpStreamProcessBound$")
			if size > 64<<10 {
				if !errors.Is(err, errDumpStreamLimit) || ctx.Err() != nil {
					t.Fatalf("limit failed to stop child: %v context=%v", err, ctx.Err())
				}
				if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("incomplete output remains: %v", err)
				}
			} else {
				raw, readErr := os.ReadFile(dst)
				if err != nil || readErr != nil || len(raw) != size {
					t.Fatalf("exact boundary: %v %v size=%d", err, readErr, len(raw))
				}
			}
		})
	}
}

func TestDumpStreamCleanupAndExclusiveCreate(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "output")
	missing := filepath.Join(root, "missing-input")
	if err := runToFile(context.Background(), dst, &missing, 10, "unused"); err == nil {
		t.Fatal("accepted missing input")
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output remains: %v", err)
	}
	if err := os.WriteFile(dst, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runToFile(context.Background(), dst, nil, 10, "unused"); err == nil {
		t.Fatal("overwrote existing output")
	}
	raw, err := os.ReadFile(dst)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("existing data changed: %q %v", raw, err)
	}
}

func TestDumpFilterStreamLimit(t *testing.T) {
	requireLoadDumpTools(t)
	root := t.TempDir()
	dump := realDumpBytes(t, root, map[string]string{"a.txt": strings.Repeat("data", 1024)})
	src := filepath.Join(root, "source.dump")
	if err := os.WriteFile(src, dump, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := testDumpLoadService(root, "", "")
	svc.MaxDumpBytes = 16
	dst := filepath.Join(root, "filtered.dump")
	if err := svc.filterIgnored(context.Background(), src, dst); !errors.Is(err, errDumpStreamLimit) || !strings.Contains(err.Error(), "max_dump_size") {
		t.Fatalf("filter did not enforce configured cap: %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial filter output remains: %v", err)
	}
	svc.MaxDumpBytes = 0
	if err := svc.filterIgnored(context.Background(), src, dst); err != nil {
		t.Fatalf("zero should disable configured cap: %v", err)
	}
}

func TestDumpLoadExpandedStreamLimitKeepsCarrier(t *testing.T) {
	if !svnrotate.Supported() {
		t.Skip("generation loading is Unix-only")
	}
	requireLoadDumpTools(t)
	root := t.TempDir()
	plain := realDumpBytes(t, root, map[string]string{"large.txt": strings.Repeat("compressible data\n", 32<<10)})
	source := filepath.Join(root, "delta-source")
	if out, err := exec.Command("svnadmin", "create", source).CombinedOutput(); err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	load := exec.Command("svnadmin", "load", "--quiet", source)
	load.Stdin = bytes.NewReader(plain)
	if out, err := load.CombinedOutput(); err != nil {
		t.Fatalf("load source: %v %s", err, out)
	}
	// Repository copies carry only copy-from metadata in the incoming dump.
	// A non-incremental dump of r2 must materialize all three files instead.
	wc := filepath.Join(root, "copy-wc")
	if out, err := exec.Command("svn", "checkout", "-q", svnurl.File(source), wc).CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v %s", err, out)
	}
	for _, name := range []string{"copy1.txt", "copy2.txt"} {
		if out, err := exec.Command("svn", "copy", filepath.Join(wc, "large.txt"), filepath.Join(wc, name)).CombinedOutput(); err != nil {
			t.Fatalf("copy: %v %s", err, out)
		}
	}
	if out, err := exec.Command("svn", "commit", "-q", "-m", "copies", wc).CombinedOutput(); err != nil {
		t.Fatalf("commit copies: %v %s", err, out)
	}
	delta, err := exec.Command("svnadmin", "dump", "--quiet", "--deltas", source).Output()
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(len(delta)) + 16<<10
	expanded, err := exec.Command("svnadmin", "dump", "--quiet", "-r", "2:2", source).Output()
	if err != nil || limit >= int64(len(expanded)) {
		t.Fatalf("fixture did not expand: delta=%d expanded=%d err=%v", len(delta), len(expanded), err)
	}
	serviceWC := filepath.Join(root, "service")
	realm, repoID := uuid.NewString(), uuid.NewString()
	repos := buildCarrierRepo(t, root, serviceWC, repoID, realm, delta, "carrier.dump")
	svc := testDumpLoadService(root, serviceWC, repos)
	svc.MaxDumpBytes = limit
	repo := filepath.Join(repos, repoID)
	before, err := exec.Command("svnlook", "uuid", repo).Output()
	if err != nil {
		t.Fatal(err)
	}
	keep := 1
	_, err = svc.Load(context.Background(), realm, repoID, uuid.NewString(), false, &keep)
	if !errors.Is(err, errDumpStreamLimit) || !strings.Contains(err.Error(), "max_dump_size") {
		t.Fatalf("expanded stream should be refused: %v", err)
	}
	requireCarrierUntouched(t, svc, repo)
	after, err := exec.Command("svnlook", "uuid", repo).Output()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("generation changed: %q %q %v", before, after, err)
	}
	carrier, err := exec.Command("svnlook", "cat", "-r", "1", repo, "carrier.dump").Output()
	if err != nil || !bytes.Equal(carrier, delta) {
		t.Fatalf("carrier data changed: %v", err)
	}
	if entries, err := os.ReadDir(svc.ArchiveDir); (err != nil && !errors.Is(err, os.ErrNotExist)) || len(entries) != 0 {
		t.Fatalf("refused import created archive: %v %v", entries, err)
	}
}
