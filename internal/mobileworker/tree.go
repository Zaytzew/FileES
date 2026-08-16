package mobileworker

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	v1 "filees/pkg/mobile/v1"
)

const (
	maxTreeFiles        = 5000
	maxTreeUncompressed = 2 << 30
)

// errNotTreePack is a zip that is a repository artifact (or any other
// unmarked archive), not FileES wire packaging. The worker must not unpack it.
var errNotTreePack = errors.New("not a filees tree pack")

// errTreePayloadCorrupt is a header/body mismatch: the zip that arrived is
// not the zip the client hashed. Nothing from it may be committed.
var errTreePayloadCorrupt = errors.New("tree payload corrupt: sha256 or size mismatch")

// UploadTree unpacks a zip-on-wire folder and commits its files under
// parent_path (must be mobile-uploads/…) in one revision. Same-hash
// existing files are skipped; different hash overwrites HEAD.
func (a Appender) UploadTree(ctx context.Context, clientID, requestID string, p v1.UploadTreePayload, content io.Reader) (v1.UploadTreeResult, error) {
	view, err := a.Authority.Resolve(ctx, clientID, p.RepoID)
	if err != nil {
		return v1.UploadTreeResult{}, err
	}
	if view.Access != "rw" {
		return v1.UploadTreeResult{}, ErrAccessDenied
	}
	parent := strings.Trim(p.ParentPath, "/")
	if parent != "mobile-uploads" && !strings.HasPrefix(parent, "mobile-uploads/") {
		return v1.UploadTreeResult{}, errors.New("parent_path must be under mobile-uploads/")
	}

	if rec, err := a.Ledger.Lookup(requestID); err != nil {
		return v1.UploadTreeResult{}, err
	} else if rec != nil && rec.State == v1.OpStateCommitted {
		if rec.PayloadHash != p.Sha256 {
			return v1.UploadTreeResult{}, errors.New("request_id reused with a different payload")
		}
		return v1.UploadTreeResult{FileCount: p.FileCount, Size: p.Size, Revision: rec.Revision}, nil
	}

	spool, sum, size, err := a.spool(content)
	if err != nil {
		return v1.UploadTreeResult{}, err
	}
	defer os.Remove(spool)
	// Hash and size are checked before unpack or commit. A transport
	// bit-flip must not become a silent tree in HEAD.
	if sum != p.Sha256 || (p.Size != 0 && p.Size != size) {
		return v1.UploadTreeResult{}, errTreePayloadCorrupt
	}

	extracted, unpackDir, err := unpackTreePack(spool)
	if err != nil {
		return v1.UploadTreeResult{}, err
	}
	defer os.RemoveAll(unpackDir)

	rev, err := a.Reader.Youngest(ctx, view.RepoPath)
	if err != nil {
		return v1.UploadTreeResult{}, err
	}
	if kind, exists, err := a.Reader.Stat(ctx, view.RepoPath, parent, rev); err != nil {
		return v1.UploadTreeResult{}, err
	} else if exists && kind != v1.KindDirectory {
		return v1.UploadTreeResult{}, errors.New("parent path is not a directory")
	}

	var toCommit []TreeFile
	for _, item := range extracted {
		target := path.Join(parent, item.RelPath)
		kind, exists, err := a.Reader.Stat(ctx, view.RepoPath, target, rev)
		if err != nil {
			return v1.UploadTreeResult{}, err
		}
		if !exists {
			toCommit = append(toCommit, item)
			continue
		}
		if kind != v1.KindFile {
			return v1.UploadTreeResult{}, fmt.Errorf("path %q is not a file", target)
		}
		_, existing, err := a.Reader.Cat(ctx, view.RepoPath, target, rev, io.Discard)
		if err != nil {
			return v1.UploadTreeResult{}, err
		}
		if existing == item.Sha {
			continue
		}
		item.Replace = true
		toCommit = append(toCommit, item)
	}

	base := Record{RequestID: requestID, ClientID: clientID, RepoID: p.RepoID, Path: parent, PayloadHash: sum, State: v1.OpStateCommitting}
	if err := a.Ledger.Put(base); err != nil {
		return v1.UploadTreeResult{}, err
	}

	newRev, err := a.Committer.CommitTree(ctx, view.RepoPath, parent, toCommit, requestID)
	if err != nil {
		a.reject(base)
		return v1.UploadTreeResult{}, err
	}

	done := base
	done.State = v1.OpStateCommitted
	done.Revision = newRev
	done.FinalPath = parent
	if err := a.Ledger.Put(done); err != nil {
		return v1.UploadTreeResult{}, err
	}
	return v1.UploadTreeResult{FileCount: len(extracted), Size: size, Revision: newRev}, nil
}

func unpackTreePack(zipPath string) ([]TreeFile, string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, "", err
	}
	defer reader.Close()
	if reader.Comment != v1.TreePackComment {
		return nil, "", errNotTreePack
	}

	dest, err := os.MkdirTemp("", "filees-mobile-unzip-")
	if err != nil {
		return nil, "", err
	}
	var files []TreeFile
	var uncompressed int64
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		rel, err := sanitizeZipPath(entry.Name)
		if err != nil {
			os.RemoveAll(dest)
			return nil, "", err
		}
		if skipZipName(rel) {
			continue
		}
		if len(files) >= maxTreeFiles {
			os.RemoveAll(dest)
			return nil, "", fmt.Errorf("zip exceeds %d files", maxTreeFiles)
		}
		uncompressed += int64(entry.UncompressedSize64)
		if uncompressed > maxTreeUncompressed {
			os.RemoveAll(dest)
			return nil, "", errors.New("zip uncompressed size exceeds limit")
		}
		outPath := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
			os.RemoveAll(dest)
			return nil, "", err
		}
		sum, err := extractZipFile(entry, outPath)
		if err != nil {
			os.RemoveAll(dest)
			return nil, "", err
		}
		files = append(files, TreeFile{RelPath: rel, SpoolPath: outPath, Sha: sum})
	}
	if len(files) == 0 {
		os.RemoveAll(dest)
		return nil, "", errors.New("zip contained no files")
	}
	return files, dest, nil
}

func sanitizeZipPath(name string) (string, error) {
	rel := strings.ReplaceAll(name, "\\", "/")
	rel = strings.TrimPrefix(rel, "/")
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("zip path escapes archive: %q", name)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("zip path invalid: %q", name)
		}
	}
	return rel, nil
}

func skipZipName(rel string) bool {
	base := path.Base(rel)
	if strings.HasPrefix(base, ".") {
		return true
	}
	return strings.HasPrefix(rel, "__MACOSX/") || rel == "__MACOSX"
}

func extractZipFile(entry *zip.File, dest string) (string, error) {
	in, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
