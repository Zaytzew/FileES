package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func requireAbsolutePath(path string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("path %q is not absolute", path)
	}
	return nil
}

func requirePathInsideRoot(path, root string) error {
	if err := requireAbsolutePath(path); err != nil {
		return err
	}
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	relative, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside root %q", path, root)
	}
	return nil
}

func validateAutostartID(id string) error {
	if id == "" {
		return errors.New("autostart ID is required")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("invalid autostart ID %q", id)
	}
	return nil
}

func splitPickerOutput(output string) []string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		if path := strings.TrimSpace(line); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// commandCancelled reports whether the error from a subprocess represents a
// deliberate user cancellation (exit code 1 by convention across all pickers).
func commandCancelled(err error) bool {
	var exitErr interface{ ExitCode() int }
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}
