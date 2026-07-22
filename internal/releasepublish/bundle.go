package releasepublish

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func BundleDirectory(source, output string) error {
	root, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("bundle source must be a directory")
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle source contains symlink: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("bundle source contains special file: %s", path)
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(paths, func(i, j int) bool {
		left, _ := filepath.Rel(root, paths[i])
		right, _ := filepath.Rel(root, paths[j])
		return filepath.ToSlash(left) < filepath.ToSlash(right)
	})
	if len(paths) == 0 {
		return errors.New("bundle source is empty")
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if relative, err := filepath.Rel(root, output); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("bundle output must be outside the source directory")
	}
	dir := filepath.Dir(output)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".client-bundle-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	gz, err := gzip.NewWriterLevel(temp, gzip.BestCompression)
	if err != nil {
		temp.Close()
		return err
	}
	gz.Header.ModTime = time.Unix(0, 0).UTC()
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	writeErr := writeBundleEntries(tw, root, paths)
	closeTarErr := tw.Close()
	closeGzipErr := gz.Close()
	syncErr := temp.Sync()
	closeErr := temp.Close()
	for _, candidate := range []error{writeErr, closeTarErr, closeGzipErr, syncErr, closeErr} {
		if candidate != nil {
			return candidate
		}
	}
	if err := os.Rename(tempPath, output); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func writeBundleEntries(writer *tar.Writer, root string, paths []string) error {
	for _, filePath := range paths {
		info, err := os.Stat(filePath)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		mode := int64(0o644)
		if info.IsDir() {
			mode = 0o755
			name = strings.TrimSuffix(name, "/") + "/"
		} else if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		header := &tar.Header{
			Name: name, Mode: mode, Uid: 0, Gid: 0, Size: info.Size(),
			ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{}, Format: tar.FormatUSTAR,
		}
		if info.IsDir() {
			header.Typeflag = tar.TypeDir
			header.Size = 0
		} else {
			header.Typeflag = tar.TypeReg
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			continue
		}
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
