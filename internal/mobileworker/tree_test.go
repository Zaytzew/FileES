package mobileworker

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

func packTree(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := zw.SetComment(v1.TreePackComment); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadTree(t *testing.T, a Appender, files map[string][]byte) v1.UploadTreeResult {
	t.Helper()
	body := packTree(t, files)
	res, err := a.UploadTree(context.Background(), "c", uuid.NewString(), v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: len(files), Size: int64(len(body)), Sha256: sha(body),
	}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func catRepo(t *testing.T, repo, rel string) []byte {
	t.Helper()
	out, err := exec.Command("svnlook", "cat", repo, rel).Output()
	if err != nil {
		t.Fatalf("svnlook cat %s: %v", rel, err)
	}
	return out
}

func TestUploadTreeCommitsNestedFiles(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	res := uploadTree(t, a, map[string][]byte{
		"album/a.txt": []byte("alpha"),
		"album/b.txt": []byte("beta"),
	})
	if res.FileCount != 2 || res.Revision < 2 {
		t.Fatalf("result = %+v", res)
	}
	if got := string(catRepo(t, repo, "mobile-uploads/album/a.txt")); got != "alpha" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := string(catRepo(t, repo, "mobile-uploads/album/b.txt")); got != "beta" {
		t.Fatalf("b.txt = %q", got)
	}
}

func TestUploadTreeSkipsIdenticalAndOverwritesDifferent(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	first := uploadTree(t, a, map[string][]byte{"note.txt": []byte("v1")})
	same := uploadTree(t, a, map[string][]byte{"note.txt": []byte("v1")})
	if same.Revision != first.Revision {
		t.Fatalf("identical replay advanced HEAD %d -> %d", first.Revision, same.Revision)
	}
	changed := uploadTree(t, a, map[string][]byte{"note.txt": []byte("v2")})
	if changed.Revision <= first.Revision {
		t.Fatalf("overwrite did not commit: %+v", changed)
	}
	if got := string(catRepo(t, repo, "mobile-uploads/note.txt")); got != "v2" {
		t.Fatalf("after overwrite = %q", got)
	}
}

func TestUploadTreeKeepsInnerZipAsFile(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	var inner bytes.Buffer
	iw := zip.NewWriter(&inner)
	w, err := iw.Create("secret/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("do not explode")); err != nil {
		t.Fatal(err)
	}
	if err := iw.Close(); err != nil {
		t.Fatal(err)
	}
	artifact := inner.Bytes()

	uploadTree(t, a, map[string][]byte{
		"docs/projekt.zip": artifact,
		"docs/note.txt":    []byte("ok"),
	})
	if got := catRepo(t, repo, "mobile-uploads/docs/projekt.zip"); !bytes.Equal(got, artifact) {
		t.Fatalf("inner zip mutated or unpacked, %d bytes", len(got))
	}
	if _, err := exec.Command("svnlook", "cat", repo, "mobile-uploads/docs/projekt.zip/secret/readme.txt").Output(); err == nil {
		t.Fatal("worker recursively unpacked an inner repository zip")
	}
}

func TestUploadTreeRejectsUnmarkedArtifact(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("package main")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	_, err = a.UploadTree(context.Background(), "c", uuid.NewString(), v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body),
	}, bytes.NewReader(body))
	if err == nil || !strings.Contains(err.Error(), "not a filees tree pack") {
		t.Fatalf("expected unmarked zip rejection, got %v", err)
	}
	if _, err := exec.Command("svnlook", "cat", repo, "mobile-uploads/src/main.go").Output(); err == nil {
		t.Fatal("unmarked artifact zip was exploded into the repo")
	}
}

func TestUploadTreeRejectsCorruptPayload(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")
	head, err := SVNReader{}.Youngest(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	body := packTree(t, map[string][]byte{"hay.txt": []byte("this must not land")})
	_, err = a.UploadTree(context.Background(), "c", uuid.NewString(), v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)),
		Sha256: sha([]byte("not-the-zip")),
	}, bytes.NewReader(body))
	if !errors.Is(err, errTreePayloadCorrupt) {
		t.Fatalf("expected errTreePayloadCorrupt, got %v", err)
	}
	got, err := SVNReader{}.Youngest(context.Background(), repo)
	if err != nil || got != head {
		t.Fatalf("corrupt zip moved HEAD %d -> %d (%v)", head, got, err)
	}
	if _, err := exec.Command("svnlook", "cat", repo, "mobile-uploads/hay.txt").Output(); err == nil {
		t.Fatal("corrupt payload was committed")
	}
}

func TestUploadTreeRejectsZipSlip(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := zw.SetComment(v1.TreePackComment); err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create("../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("nope")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	_, err = a.UploadTree(context.Background(), "c", uuid.NewString(), v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body),
	}, bytes.NewReader(body))
	if err == nil {
		t.Fatal("expected zip-slip rejection")
	}
}

func TestDispatchUploadTree(t *testing.T) {
	requireSVN(t)
	d := newDispatcher(t, newSeededRepo(t), "rw")
	body := packTree(t, map[string][]byte{"x.txt": []byte("via dispatcher")})
	frame := frameRequest(t, uuid.NewString(), v1.OpUploadTree, v1.UploadTreePayload{
		RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body),
	}, body)
	resp, _ := serve(t, d, frame)
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %s error = %+v", resp.Status, resp.Error)
	}
	var res v1.UploadTreeResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.FileCount != 1 || res.Revision < 2 {
		t.Fatalf("result = %+v", res)
	}
}
