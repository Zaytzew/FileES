package clientupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/releaseenvelope"
)

type artifactFetcher map[string][]byte

func (fetcher artifactFetcher) Cat(_ context.Context, path string) ([]byte, error) {
	data, ok := fetcher[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return data, nil
}

type tarEntry struct {
	name     string
	typeflag byte
	mode     int64
	data     string
}

func makeBundle(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Mode: entry.mode, Size: int64(len(entry.data))}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.data != "" {
			if _, err := tw.Write([]byte(entry.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func stagedRelease(bundle []byte) *releaseenvelope.Resolved {
	digest := sha256.Sum256(bundle)
	return &releaseenvelope.Resolved{
		Component: releaseenvelope.Component{Manifest: "releases/r1/desktop/linux-amd64/manifest.json"},
		Manifest: &releaseenvelope.ArtifactManifest{Artifacts: []releaseenvelope.Artifact{{
			Source: "client.tar.gz", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(bundle)), Kind: "bundle",
		}}},
	}
}

func TestBundleStagerVerifiesThenExtractsRegularFiles(t *testing.T) {
	bundle := makeBundle(t,
		tarEntry{name: "bin/", typeflag: tar.TypeDir, mode: 0o755},
		tarEntry{name: "bin/filees", typeflag: tar.TypeReg, mode: 0o755, data: "binary"},
		tarEntry{name: "VERSION", typeflag: tar.TypeReg, mode: 0o644, data: "1.1\n"},
	)
	resolved := stagedRelease(bundle)
	path := "releases/r1/desktop/linux-amd64/client.tar.gz"
	staged, err := (BundleStager{Fetcher: artifactFetcher{path: bundle}, Root: t.TempDir()}).Stage(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	root := staged.Root
	defer staged.Remove()
	data, err := os.ReadFile(filepath.Join(root, "bin", "filees"))
	if err != nil || string(data) != "binary" {
		t.Fatalf("staged binary = %q, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(root, "bin", "filees"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode = %v, %v", info.Mode(), err)
	}
	if err := staged.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("staging survived cleanup: %v", err)
	}
}

func TestBundleStagerRejectsHashSizeAndMultipleBundles(t *testing.T) {
	bundle := makeBundle(t, tarEntry{name: "VERSION", typeflag: tar.TypeReg, data: "1"})
	resolved := stagedRelease(bundle)
	path := "releases/r1/desktop/linux-amd64/client.tar.gz"
	resolved.Manifest.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if _, err := (BundleStager{Fetcher: artifactFetcher{path: bundle}, Root: t.TempDir()}).Stage(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("hash mismatch error = %v", err)
	}
	resolved = stagedRelease(bundle)
	resolved.Manifest.Artifacts[0].Size++
	if _, err := (BundleStager{Fetcher: artifactFetcher{path: bundle}, Root: t.TempDir()}).Stage(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("size mismatch error = %v", err)
	}
	resolved = stagedRelease(bundle)
	resolved.Manifest.Artifacts = append(resolved.Manifest.Artifacts, resolved.Manifest.Artifacts[0])
	if _, err := (BundleStager{Fetcher: artifactFetcher{}, Root: t.TempDir()}).Stage(context.Background(), resolved); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("multiple bundle error = %v", err)
	}
}

func TestExtractorRejectsTraversalLinksDuplicatesAndExpansionLimits(t *testing.T) {
	cases := []struct {
		name    string
		bundle  []byte
		maxFile int
		maxSize int64
	}{
		{name: "traversal", bundle: makeBundle(t, tarEntry{name: "../escape", typeflag: tar.TypeReg, data: "x"}), maxFile: 10, maxSize: 10},
		{name: "symlink", bundle: makeBundle(t, tarEntry{name: "link", typeflag: tar.TypeSymlink}), maxFile: 10, maxSize: 10},
		{name: "duplicate", bundle: makeBundle(t, tarEntry{name: "a", typeflag: tar.TypeReg, data: "x"}, tarEntry{name: "a", typeflag: tar.TypeReg, data: "x"}), maxFile: 10, maxSize: 10},
		{name: "files", bundle: makeBundle(t, tarEntry{name: "a", typeflag: tar.TypeReg, data: "x"}, tarEntry{name: "b", typeflag: tar.TypeReg, data: "x"}), maxFile: 1, maxSize: 10},
		{name: "expanded-size", bundle: makeBundle(t, tarEntry{name: "a", typeflag: tar.TypeReg, data: "xx"}), maxFile: 10, maxSize: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := extractTarGzip(test.bundle, t.TempDir(), test.maxFile, test.maxSize); err == nil {
				t.Fatal("unsafe bundle accepted")
			}
		})
	}
}
