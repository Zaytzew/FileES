//go:build !windows

package servertool

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"filees/pkg/clientview"
	v1 "filees/pkg/mobile/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMobileConfiguredTempUpload(t *testing.T) {
	if isolateSandboxingTest(t, "TestMobileConfiguredTempUpload") {
		return
	}
	f := newMobileWorkerFixture(t)
	tempRoot := sandboxTempDir(t)
	ledger := sandboxTempDir(t)
	blocked := filepath.Join(sandboxTempDir(t), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	clientID, repoID := uuid.NewString(), uuid.NewString()
	newMobileSeededRepoAt(t, filepath.Join(f.repositoriesRoot, repoID))
	writeMobileClientView(t, f.serviceWC, clientID, f.realmID, 9, []clientview.Repository{mobileGrantedRepository(repoID, "rw")})
	raw, _ := os.ReadFile(f.configPath)
	var conf map[string]any
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	conf["mobile"] = map[string]any{"temp_root": tempRoot}
	raw, _ = json.Marshal(conf)
	if err := os.WriteFile(f.configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := serverconfig.LoadFor(f.configPath, 0)
	if err != nil || cfg.Mobile.TempRoot != tempRoot {
		t.Fatalf("config: %+v %v", cfg.Mobile, err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Repositories.ResultsRoot, "gui-blobs"), 0700); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range capacityPaths(cfg) {
		if p.Label == "mobile-temp" && p.Name == tempRoot {
			found = true
		}
	}
	if !found {
		t.Fatal("capacity monitor omitted configured mobile filesystem")
	}
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	_ = zw.SetComment(v1.TreePackComment)
	w, _ := zw.Create("temp-proof.bin")
	_, _ = w.Write(bytes.Repeat([]byte("payload"), 1024*128))
	_ = zw.Close()
	sum := sha256.Sum256(body.Bytes())
	req, _ := v1.NewRequest(uuid.NewString(), v1.OpUploadTree, v1.UploadTreePayload{RepoID: repoID, ParentPath: "mobile-uploads", FileCount: 1, Size: int64(body.Len()), Sha256: hex.EncodeToString(sum[:])})
	header, _ := json.Marshal(req)
	var in, out, stderr bytes.Buffer
	if err := v1.WriteFrame(&in, v1.RequestMagic, header, body.Bytes()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked)
	if code := runMobileEntry(f.configPath, ledger, []string{"op", clientID}, mobileOperationalGetenv, &in, &out, &stderr, mobileNeverExec(t)); code != ExitOK {
		t.Fatalf("exit=%d %s", code, &stderr)
	}
	h, _, err := v1.ReadFrame(&out, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := v1.ParseResponse(h)
	if err != nil || resp.Status != v1.StatusOK {
		t.Fatalf("response=%+v err=%v stderr=%s", resp, err, &stderr)
	}
	if got := os.Getenv("TMPDIR"); got != blocked {
		t.Fatalf("environment not restored: %q", got)
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("workspace cleanup: %v %v", entries, err)
	}
	if runtime.GOOS == "openbsd" {
		file, err := os.CreateTemp("/tmp", "filees-outside-mobile-")
		if err == nil {
			file.Close()
			os.Remove(file.Name())
			t.Fatal("sandbox still permits default /tmp")
		}
	}
}

func TestMobileTempRefusesInvalidRootWithoutFallback(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", root)
	for _, bad := range []string{"relative", "/", file, public, link, filepath.Join(root, "missing")} {
		if restore, err := prepareMobileTemp(bad); err == nil {
			restore()
			t.Fatalf("accepted %q", bad)
		}
		if os.Getenv("TMPDIR") != root {
			t.Fatal("changed environment on failure")
		}
	}
	restore, err := prepareMobileTemp("")
	if err != nil {
		t.Fatal(err)
	}
	restore()
}
