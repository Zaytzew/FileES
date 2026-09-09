package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const workingCopyIdentitySchema = "filees.working-copy/v1"

type workingCopyIdentity struct {
	Schema   string `json:"schema"`
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	RepoURL  string `json:"repo_url"`
}

func expectedWorkingCopyIdentity(serverID, repoID, repoURL string) workingCopyIdentity {
	return workingCopyIdentity{Schema: workingCopyIdentitySchema, ServerID: serverID, RepoID: repoID, RepoURL: repoURL}
}

func workingCopyIdentityPath(root string) string {
	return filepath.Join(root, ".filees", "state", "working-copy.json")
}

func validateWorkingCopyIdentity(root string, expected workingCopyIdentity) error {
	if err := validateIdentityPath(root); err != nil {
		return err
	}
	raw, err := os.ReadFile(workingCopyIdentityPath(root))
	if errors.Is(err, os.ErrNotExist) {
		// Pre-marker working copies are migrated only after SVN URL validation.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read working-copy identity: %w", err)
	}
	var actual workingCopyIdentity
	if err := json.Unmarshal(raw, &actual); err != nil {
		return errors.New("working-copy identity is invalid")
	}
	if actual.Schema != workingCopyIdentitySchema || actual.ServerID != expected.ServerID || actual.RepoID != expected.RepoID || actual.RepoURL != expected.RepoURL {
		return errors.New("working-copy identity belongs to another FileES attachment")
	}
	return nil
}

// Do not follow a transplanted marker or an alias/junction while granting the
// native mutation guard. Existing metadata must be plain files/directories.
func validateIdentityPath(root string) error {
	path, err := filepath.Abs(workingCopyIdentityPath(root))
	if err != nil {
		return err
	}
	leaf := path
	for {
		st, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (st.Mode()&os.ModeSymlink != 0 || (path == leaf && !st.Mode().IsRegular()) || (path != leaf && !st.IsDir())) {
			return fmt.Errorf("working-copy identity requires a plain path: %s", path)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func ensureWorkingCopyIdentity(root string, expected workingCopyIdentity) error {
	if err := validateWorkingCopyIdentity(root, expected); err != nil {
		return err
	}
	path := workingCopyIdentityPath(root)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if strings.TrimSpace(expected.ServerID) == "" || strings.TrimSpace(expected.RepoID) == "" || strings.TrimSpace(expected.RepoURL) == "" {
		return errors.New("working-copy identity is incomplete")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(dir, ".working-copy-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
