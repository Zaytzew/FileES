package mobileworker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

type treeCountingReader struct {
	AppendReader
	catRevisions  []int64
	sizeRevisions []int64
	sizeError     error
}

func (r *treeCountingReader) FileSize(ctx context.Context, repo, p string, rev int64) (int64, error) {
	r.sizeRevisions = append(r.sizeRevisions, rev)
	if r.sizeError != nil {
		return 0, r.sizeError
	}
	return r.AppendReader.FileSize(ctx, repo, p, rev)
}

func (r *treeCountingReader) Cat(ctx context.Context, repo, p string, rev int64, w io.Writer) (int64, string, error) {
	r.catRevisions = append(r.catRevisions, rev)
	return r.AppendReader.Cat(ctx, repo, p, rev, w)
}

func TestUploadTreeSizeGate(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "openbsd" {
		t.Skip("upload capacity monitor requires Linux or OpenBSD")
	}
	requireSVN(t)
	for _, tc := range []struct {
		name, old, incoming string
		wantCat             int
		wantCommit          bool
	}{
		{"different-size", "long old content", "short", 0, true},
		{"same-content", "same", "same", 1, false},
		{"same-size-different-content", "old!", "new!", 1, true},
		{"both-empty", "", "", 1, false},
		{"becomes-empty", "old", "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newSeededRepo(t)
			a := newAppender(t, repo, "rw")
			first := uploadTree(t, a, map[string][]byte{"note.txt": []byte(tc.old)})
			reader := &treeCountingReader{AppendReader: a.Reader}
			a.Reader = reader
			result := uploadTree(t, a, map[string][]byte{"note.txt": []byte(tc.incoming)})
			if len(reader.sizeRevisions) != 1 || reader.sizeRevisions[0] != first.Revision {
				t.Fatalf("FileSize revisions = %v, want [%d]", reader.sizeRevisions, first.Revision)
			}
			if len(reader.catRevisions) != tc.wantCat {
				t.Fatalf("Cat called %d times, want %d", len(reader.catRevisions), tc.wantCat)
			}
			for _, rev := range reader.catRevisions {
				if rev != first.Revision {
					t.Fatalf("Cat revision %d, want %d", rev, first.Revision)
				}
			}
			if (result.Revision > first.Revision) != tc.wantCommit {
				t.Fatalf("revision %d -> %d, wantCommit=%v", first.Revision, result.Revision, tc.wantCommit)
			}
			if got := string(catRepo(t, repo, "mobile-uploads/note.txt")); got != tc.incoming {
				t.Fatalf("content = %q, want %q", got, tc.incoming)
			}
		})
	}
}

func TestUploadTreeSizeErrorDoesNotCommit(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "openbsd" {
		t.Skip("upload capacity monitor requires Linux or OpenBSD")
	}
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")
	first := uploadTree(t, a, map[string][]byte{"note.txt": []byte("old")})
	wantErr := errors.New("metadata unavailable")
	reader := &treeCountingReader{AppendReader: a.Reader, sizeError: wantErr}
	a.Reader = reader
	body := packTree(t, map[string][]byte{"note.txt": []byte("new content")})
	id := uuid.NewString()
	_, err := a.UploadTree(context.Background(), "c", id, v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body),
	}, bytes.NewReader(body))
	if !errors.Is(err, wantErr) || len(reader.catRevisions) != 0 {
		t.Fatalf("err=%v, Cat calls=%v", err, reader.catRevisions)
	}
	if head, err := a.Reader.Youngest(context.Background(), repo); err != nil || head != first.Revision {
		t.Fatalf("HEAD=%d, err=%v, want %d", head, err, first.Revision)
	}
	if got := string(catRepo(t, repo, "mobile-uploads/note.txt")); got != "old" {
		t.Fatalf("changed after error: %q", got)
	}
}

func TestSVNReaderFileSizeUsesRevisionAndLiteralPath(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	commitFileInto(t, repo, "-t", []byte("four"))
	reader := SVNReader{}
	ctx := context.Background()
	withFile, err := reader.Youngest(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if size, err := reader.FileSize(ctx, repo, "-t", withFile); err != nil || size != 4 {
		t.Fatalf("size=%d, err=%v", size, err)
	}
	if _, err := reader.FileSize(ctx, repo, "-t", withFile-1); err == nil {
		t.Fatal("read a path absent from the requested revision")
	}
	if _, err := reader.FileSize(ctx, repo, "docs", withFile); err == nil {
		t.Fatal("directory accepted as a file")
	}
}
