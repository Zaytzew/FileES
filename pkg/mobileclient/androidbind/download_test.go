package androidbind

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"filees/pkg/mobileclient"

	"golang.org/x/crypto/ssh"
)

// The object travels over a real SSH session into a part file and lands
// under destPath only after its size and sha256 match the worker's header.
func TestDownloadToStreamsOverSSH(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	hostSigner := generateEd25519(t)
	storeDir := t.TempDir()
	hostKey := string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))
	probe, err := NewClient(storeDir, "unused:0", "filees-mobile-v1", hostKey)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(probe.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	addr := startDispatcherServer(t, hostSigner, clientPub, newDispatcher(repo))
	client, err := NewClient(storeDir, addr, "filees-mobile-v1", hostKey)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "a.jpg")
	if err := client.DownloadTo("repo-1", "photos/a.jpg", dest); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "hello" {
		t.Fatalf("downloaded %q, %v", got, err)
	}
	if _, err := os.Stat(dest + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part file left behind: %v", err)
	}
}

type generatedDownload struct {
	size    int64
	corrupt bool
}

func (g generatedDownload) Do(context.Context, v1.Request, []byte) (v1.Response, []byte, error) {
	panic("buffered download used")
}
func (g generatedDownload) DoStreamTo(_ context.Context, req v1.Request, sink io.Writer) (v1.Response, error) {
	h := sha256.New()
	block := bytes.Repeat([]byte("a"), 32<<10)
	for left := g.size; left > 0; {
		n := int64(len(block))
		if n > left {
			n = left
		}
		if _, err := io.MultiWriter(sink, h).Write(block[:n]); err != nil {
			return v1.Response{}, err
		}
		left -= n
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if g.corrupt {
		sum = strings.Repeat("0", 64)
	}
	return v1.NewSuccess(req.RequestID, req.Operation, v1.ReadObjectResult{Path: "large.bin", Size: g.size, Sha256: sum})
}

func TestDownloadToLargeStreamAndPrivateStaging(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "large.bin")
	// An unrelated part file must not be truncated or deleted.
	if err := os.WriteFile(dest+".part", []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Client{inner: mobileclient.Client{Transport: generatedDownload{size: 64 << 20}}, ctx: ctx, cancel: cancel}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := c.DownloadTo("repo-1", "large.bin", dest); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if after.TotalAlloc-before.TotalAlloc > 8<<20 {
		t.Fatalf("buffered file: allocated %d", after.TotalAlloc-before.TotalAlloc)
	}
	if info, err := os.Stat(dest); err != nil || info.Size() != 64<<20 {
		t.Fatal("incorrect file size", err)
	}
	if got, _ := os.ReadFile(dest + ".part"); string(got) != "unrelated" {
		t.Fatal("touched another part")
	}
	c.inner.Transport = generatedDownload{size: 1 << 20, corrupt: true}
	if err := c.DownloadTo("repo-1", "large.bin", dest); err == nil {
		t.Fatal("accepted invalid hash")
	}
	if info, _ := os.Stat(dest); info.Size() != 64<<20 {
		t.Fatal("corrupt download changed destination")
	}
	c.inner.Transport = generatedDownload{size: 1024}
	if err := c.DownloadTo("repo-1", "large.bin", dest); err != nil {
		t.Fatal("valid replacement failed", err)
	}
	if info, _ := os.Stat(dest); info.Size() != 1024 {
		t.Fatal("valid replacement not installed")
	}
	c.Cancel()
	if err := c.DownloadTo("repo-1", "large.bin", dest); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	if info, _ := os.Stat(dest); info.Size() != 1024 {
		t.Fatal("cancelled download changed destination")
	}
	parts, _ := filepath.Glob(filepath.Join(dir, ".filees-download-*"))
	if len(parts) != 0 {
		t.Fatal("leaked staging", parts)
	}
}

type corruptingTransport struct{}

func (corruptingTransport) Do(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
	// The header names "hello"; the body that arrives is not it.
	resp, err := v1.NewSuccess(req.RequestID, req.Operation, v1.ReadObjectResult{Path: "photos/a.jpg", Size: 5,
		Sha256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"})
	return resp, []byte("hellX"), err
}

func TestDownloadToRefusesMismatchedBytes(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "a.jpg")
	if err := os.WriteFile(dest, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{inner: mobileclient.Client{Transport: corruptingTransport{}, Store: mobileclient.Store{Root: t.TempDir()}}}
	if err := client.DownloadTo("repo-1", "photos/a.jpg", dest); err == nil {
		t.Fatal("corrupted download accepted")
	}
	if got, _ := os.ReadFile(dest); string(got) != "previous" {
		t.Fatalf("destination replaced by a bad download: %q", got)
	}
	if _, err := os.Stat(dest + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part file left behind: %v", err)
	}
}
