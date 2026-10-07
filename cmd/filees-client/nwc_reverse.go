package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// reverseReconcileNWC reads one repository revision and stages all downloaded
// files before changing the local directory. It never starts svnmucc.
func reverseReconcileNWC(ctx context.Context, remote nwcRemote, rev int64, head map[string]nwcEntry, local map[string]nwcLocal, root, workDir string) error {
	temp, err := os.MkdirTemp(workDir, ".nwc-reverse-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	paths := make([]string, 0, len(head))
	for rel := range head {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	staged := map[string]string{}
	unchanged := map[string]bool{}
	for _, rel := range paths {
		if head[rel].kind != "file" {
			continue
		}
		spool := filepath.Join(temp, fmt.Sprintf("%d.bin", len(staged)))
		f, err := os.OpenFile(spool, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		h := sha256.New()
		copyErr := remote.cat(ctx, rel, rev, io.MultiWriter(f, h))
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return fmt.Errorf("download %s: %w", rel, err)
		}
		staged[rel] = spool
		if item, ok := local[rel]; ok && item.kind == "file" {
			localHash, err := hashNWCFile(item.path)
			if err != nil {
				return err
			}
			unchanged[rel] = localHash == hex.EncodeToString(h.Sum(nil))
		}
	}
	// Remove paths absent from HEAD, and obstructions of a different kind.
	// Descendants go first so no unrelated path is reached through a parent.
	localPaths := make([]string, 0, len(local))
	for rel := range local {
		localPaths = append(localPaths, rel)
	}
	sort.Slice(localPaths, func(i, j int) bool {
		if strings.Count(localPaths[i], "/") != strings.Count(localPaths[j], "/") {
			return strings.Count(localPaths[i], "/") > strings.Count(localPaths[j], "/")
		}
		return localPaths[i] > localPaths[j]
	})
	removed := 0
	for _, rel := range localPaths {
		item := local[rel]
		if head[rel].kind == item.kind {
			continue
		}
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove local %s: %w", rel, err)
		}
		removed++
	}
	// Directory parents before children. Empty directories in SVN are kept.
	sort.Slice(paths, func(i, j int) bool {
		if strings.Count(paths[i], "/") != strings.Count(paths[j], "/") {
			return strings.Count(paths[i], "/") < strings.Count(paths[j], "/")
		}
		return paths[i] < paths[j]
	})
	for _, rel := range paths {
		if head[rel].kind != "dir" {
			continue
		}
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o700); err != nil {
			return err
		}
	}
	written := 0
	for _, rel := range paths {
		if head[rel].kind != "file" || unchanged[rel] {
			continue
		}
		if err := replaceNWCFile(staged[rel], filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return fmt.Errorf("write local %s: %w", rel, err)
		}
		written++
	}
	fmt.Fprintf(os.Stdout, "reconciled revision=%d written=%d removed=%d\n", rev, written, removed)
	return nil
}

func replaceNWCFile(spool, target string) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	in, err := os.Open(spool)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".filees-nwc-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if err := out.Chmod(0o600); err != nil {
		out.Close()
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(out.Name(), target); err == nil {
		return nil
	} else if _, statErr := os.Lstat(target); statErr != nil {
		return err
	}
	// Windows rename of an open destination can fail; a failed replacement is
	// visible to the caller. Never erase the existing bytes to make it pass.
	return fmt.Errorf("atomic replace unavailable for %s", target)
}
