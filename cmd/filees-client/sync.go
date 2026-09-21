package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/client"
)

type workingCopy interface {
	Update(ctx context.Context, localPath string) (string, error)
	Status(ctx context.Context, rootDirectory string, paths []string) ([]client.StatusEntry, error)
	Add(ctx context.Context, rootDirectory string, paths []string) (string, error)
	Commit(ctx context.Context, rootDirectory string, paths []string, message string) (string, error)
}

// syncPlan decides what one shot may publish. Unversioned paths are added.
// Already scheduled edits are committed. Conflict, obstruction, incomplete
// and missing paths stop the shot: this tool does not delete or merge.
func syncPlan(entries []client.StatusEntry) (add, commit []string, err error) {
	for _, entry := range entries {
		path := strings.TrimSpace(entry.Path)
		if path == "" || path == "." {
			continue
		}
		switch entry.Item {
		case "unversioned":
			add = append(add, path)
			commit = append(commit, path)
		case "added", "modified", "deleted", "replaced":
			commit = append(commit, path)
		case "conflicted", "obstructed", "incomplete", "missing":
			return nil, nil, fmt.Errorf("%s is %s", path, entry.Item)
		}
	}
	return add, commit, nil
}

func syncWorkingCopy(ctx context.Context, svn workingCopy, wc, message string) error {
	if out, err := svn.Update(ctx, wc); err != nil {
		return fmt.Errorf("update: %w", err)
	} else if strings.TrimSpace(out) != "" {
		fmt.Fprintln(os.Stdout, strings.TrimRight(out, "\n"))
	}
	entries, err := svn.Status(ctx, wc, nil)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	add, commit, err := syncPlan(entries)
	if err != nil {
		return err
	}
	files, err := expandUnversioned(wc, add)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		if _, err := svn.Add(ctx, wc, files); err != nil {
			return fmt.Errorf("add: %w", err)
		}
	}
	if len(commit) == 0 && len(files) == 0 {
		fmt.Fprintln(os.Stdout, "clean")
		return nil
	}
	// Unversioned directories are replaced by the files walked under them.
	// Committing the directory path itself would not publish those files.
	paths := append([]string{}, files...)
	for _, path := range commit {
		if containsPath(add, path) || containsPath(paths, path) {
			continue
		}
		paths = append(paths, path)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("commit message is empty")
	}
	out, err := svn.Commit(ctx, wc, paths, message)
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		fmt.Fprintln(os.Stdout, strings.TrimRight(out, "\n"))
	}
	fmt.Fprintf(os.Stdout, "committed %d\n", len(paths))
	return nil
}

// expandUnversioned turns an unversioned directory into the regular files
// under it. svn status reports only the directory, and Add uses --depth empty,
// so scheduling the directory alone would not publish the PDFs inside it.
func expandUnversioned(root string, paths []string) ([]string, error) {
	var out []string
	for _, rel := range paths {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a symlink", rel)
		}
		if !info.IsDir() {
			out = append(out, rel)
			continue
		}
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s is a symlink", path)
			}
			if d.IsDir() {
				if d.Name() == ".svn" {
					return fs.SkipDir
				}
				return nil
			}
			got, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(got))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}
