package mobileworker

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

func TestCopyUploadBytesNeverWritesBeyondLimit(t *testing.T) {
	for _, tc := range []struct {
		input string
		limit int64
		want  string
		fail  bool
	}{
		{"abc", 3, "abc", false},
		{"abcd", 3, "abc", true},
		{"x", 0, "", true},
		{"", 0, "", false},
		{"a", -1, "", true},
	} {
		var dst bytes.Buffer
		_, err := copyUploadBytes(&dst, strings.NewReader(tc.input), tc.limit)
		if errors.Is(err, errUploadLimit) != tc.fail || dst.String() != tc.want {
			t.Fatalf("limit=%d got=%q err=%v", tc.limit, dst.String(), err)
		}
	}
}

func TestUploadSpoolClosesBeforeRemovingFailure(t *testing.T) {
	a := Appender{SpoolDir: t.TempDir()}
	if _, _, _, err := a.spool(strings.NewReader("oversize"), 1); !errors.Is(err, errUploadLimit) {
		t.Fatalf("overrun: %v", err)
	}
	if entries, err := os.ReadDir(a.SpoolDir); err != nil || len(entries) != 0 {
		t.Fatalf("failed spool remains: %v %v", entries, err)
	}
	name, sum, n, err := a.spool(strings.NewReader("exact"), 5)
	if err != nil || n != 5 || sum != sha([]byte("exact")) {
		t.Fatalf("exact: %s %s %d %v", name, sum, n, err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatalf("spool still open: %v", err)
	}
}

func TestTreeUnpackUsesSpoolVolumeAndRejectsCorruption(t *testing.T) {
	root := t.TempDir()
	spool := filepath.Join(root, "input.zip")
	raw := packTree(t, map[string][]byte{"a.txt": []byte("payload")})
	if err := os.WriteFile(spool, raw, 0600); err != nil {
		t.Fatal(err)
	}
	files, unpack, err := unpackTreePack(spool)
	if err != nil || filepath.Dir(unpack) != root || len(files) != 1 {
		t.Fatalf("unpack=%q files=%v error=%v", unpack, files, err)
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	reader.File[0].CRC32 ^= 1
	dst := filepath.Join(root, "bad-file")
	if _, err := extractZipFile(reader.File[0], dst); err == nil {
		t.Fatal("accepted corrupt ZIP entry")
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt extracted file remains: %v", err)
	}
}

type oversizedBaseReader struct {
	AppendReader
	size     int64
	catCalls int
}

func (r *oversizedBaseReader) FileSize(context.Context, string, string, int64) (int64, error) {
	return r.size, nil
}

func (r *oversizedBaseReader) Cat(ctx context.Context, repo, path string, rev int64, dst io.Writer) (int64, string, error) {
	r.catCalls++
	return r.AppendReader.Cat(ctx, repo, path, rev, dst)
}

func TestUploadTreeRejectsAggregateBaseBeforeIntent(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "openbsd" {
		t.Skip("worker requires Unix capacity monitoring")
	}
	requireSVN(t)
	for _, size := range []int64{maxTreeUncompressed + 1, maxTreeUncompressed/2 + 1} {
		repo := newSeededRepo(t)
		a := newAppender(t, repo, "rw")
		first := uploadTree(t, a, map[string][]byte{"a": []byte("old-a"), "b": []byte("old-b")})
		reader := &oversizedBaseReader{AppendReader: a.Reader, size: size}
		a.Reader = reader
		raw := packTree(t, map[string][]byte{"a": []byte("new"), "b": []byte("new")})
		id := uuid.NewString()
		_, err := a.UploadTree(context.Background(), "c", id, v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", Size: int64(len(raw)), Sha256: sha(raw), FileCount: 2}, bytes.NewReader(raw))
		if !errors.Is(err, errUploadLimit) || reader.catCalls != 0 {
			t.Fatalf("size=%d err=%v cat=%d", size, err, reader.catCalls)
		}
		if rec, err := a.Ledger.Lookup(id); err != nil || rec != nil {
			t.Fatalf("intent written before refusing base budget: %+v %v", rec, err)
		}
		if head, err := a.Reader.Youngest(context.Background(), repo); err != nil || head != first.Revision {
			t.Fatalf("repository changed: head=%d err=%v", head, err)
		}
	}
}

type changingHeadCommitter struct {
	SVNAppender
	t            *testing.T
	expectedBase int64
}

func (c changingHeadCommitter) CommitTree(ctx context.Context, repo, parent string, files []TreeFile, id string) (int64, error) {
	for _, file := range files {
		if file.BaseRevision != c.expectedBase {
			c.t.Fatalf("base revision=%d want=%d", file.BaseRevision, c.expectedBase)
		}
	}
	wc := c.t.TempDir()
	run(c.t, "svn", "checkout", "-q", fileURL(repo), wc)
	file := filepath.Join(wc, "mobile-uploads", "note.txt")
	if err := os.Chmod(file, 0600); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("newer desktop edit"), 0600); err != nil {
		c.t.Fatal(err)
	}
	run(c.t, "svn", "commit", "-q", wc, "-m", "concurrent desktop change")
	return c.SVNAppender.CommitTree(ctx, repo, parent, files, id)
}

func TestUploadTreePinnedBaseDoesNotOverwriteConcurrentEdit(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "openbsd" {
		t.Skip("worker requires Unix capacity monitoring")
	}
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")
	first := uploadTree(t, a, map[string][]byte{"note.txt": []byte("old")})
	a.Committer = changingHeadCommitter{t: t, expectedBase: first.Revision}
	raw := packTree(t, map[string][]byte{"note.txt": []byte("mobile edit")})
	_, err := a.UploadTree(context.Background(), "c", uuid.NewString(), v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", Size: int64(len(raw)), Sha256: sha(raw), FileCount: 1}, bytes.NewReader(raw))
	if err == nil {
		t.Fatal("stale upload silently replaced concurrent desktop edit")
	}
	if got := string(catRepo(t, repo, "mobile-uploads/note.txt")); got != "newer desktop edit" {
		t.Fatalf("concurrent data lost: %q", got)
	}
	if head, err := a.Reader.Youngest(context.Background(), repo); err != nil || head != first.Revision+1 {
		t.Fatalf("unexpected extra revision: %d %v", head, err)
	}
}
