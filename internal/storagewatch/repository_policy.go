package storagewatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/durable"
	"github.com/google/uuid"
)

// RepositoryPolicy edits only FileES UUID repositories and remembers the exact
// previous config before replacing it. The caller holds the exclusive writer
// gate and service-WC/session barriers. It never edits authz or file modes.
type RepositoryPolicy struct{ Root, JournalRoot string }

// EnsurePrivateDirectory keeps a first administrative root run compatible
// with later monitor runs by the existing state-service owner.
func EnsurePrivateDirectory(path, ownerDirectory string) error {
	if !filepath.IsAbs(path) || !filepath.IsAbs(ownerDirectory) {
		return errors.New("private policy directories must be absolute")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privatePolicyMode(info.Mode()) {
		return errors.New("unsafe private policy directory")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	ownerErr := preservePolicyOwner(f, ownerDirectory)
	return errors.Join(ownerErr, f.Close())
}

type repositoryPolicyRecord struct {
	Schema   string `json:"schema"`
	RepoID   string `json:"repo_id"`
	Original []byte `json:"original"`
	Held     []byte `json:"held"`
	Mode     uint32 `json:"mode"`
}

func readPolicyFile(path string, limit int64) ([]byte, os.FileMode, error) {
	i, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !i.Mode().IsRegular() || i.Size() > limit {
		return nil, 0, errors.New("unsafe repository policy file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(raw)) > limit {
		return nil, 0, errors.New("repository policy file exceeds limit")
	}
	return raw, i.Mode().Perm(), nil
}

func writePolicyFile(path string, raw []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".capacity-repo-policy-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := preservePolicyOwner(f, path); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(path))
}

// This deliberately narrow editor rejects ambiguous/duplicate active options.
// Comments and all unrelated config bytes survive a successful round trip.
func readOnlyRepositoryConfig(raw []byte) ([]byte, bool, error) {
	lines := strings.SplitAfter(string(raw), "\n")
	general, seenGeneral, seenAccess, found := false, false, false, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			general = trimmed == "[general]"
			if general && seenGeneral {
				return nil, false, errors.New("duplicate SVN general section")
			}
			seenGeneral = seenGeneral || general
			continue
		}
		if !general {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "auth-access" {
			continue
		}
		if seenAccess {
			return nil, false, errors.New("duplicate SVN auth-access")
		}
		seenAccess = true
		switch strings.TrimSpace(value) {
		case "read", "none": // preserve operator policy, but still reject duplicates
		case "write":
			ending := ""
			if strings.HasSuffix(line, "\r\n") {
				ending = "\r\n"
			} else if strings.HasSuffix(line, "\n") {
				ending = "\n"
			}
			lines[i] = "auth-access = read" + ending
			found = true
		default:
			return nil, false, errors.New("unrecognized SVN auth-access")
		}
	}
	if !seenGeneral || !seenAccess {
		return nil, false, errors.New("explicit SVN general/auth-access required")
	}
	return []byte(strings.Join(lines, "")), found, nil
}

func (p RepositoryPolicy) configPath(repoID string) (string, error) {
	if !filepath.IsAbs(p.Root) || !filepath.IsAbs(p.JournalRoot) {
		return "", errors.New("repository policy roots must be absolute")
	}
	if id, err := uuid.Parse(repoID); err != nil || id.String() != repoID {
		return "", errors.New("repository policy ID must be canonical UUID")
	}
	for _, path := range []string{p.Root, filepath.Join(p.Root, repoID), filepath.Join(p.Root, repoID, "conf")} {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("repository policy directory must not be a symlink")
		}
	}
	return filepath.Join(p.Root, repoID, "conf", "svnserve.conf"), nil
}

func (p RepositoryPolicy) record(repoID string) (repositoryPolicyRecord, error) {
	var record repositoryPolicyRecord
	raw, _, err := readPolicyFile(filepath.Join(p.JournalRoot, repoID+".json"), 256<<10)
	if err != nil {
		return record, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&record); err != nil {
		return record, err
	}
	if d.Decode(new(any)) != io.EOF || record.Schema != "filees.capacity-repository-policy/v1" || record.RepoID != repoID || record.Mode > 0777 {
		return record, errors.New("invalid repository policy journal")
	}
	held, changed, err := readOnlyRepositoryConfig(record.Original)
	if err != nil || !changed || !bytes.Equal(held, record.Held) {
		return record, errors.New("invalid repository policy backup")
	}
	return record, nil
}

func (p RepositoryPolicy) Hold() error {
	if !filepath.IsAbs(p.Root) || !filepath.IsAbs(p.JournalRoot) {
		return errors.New("repository policy roots must be absolute")
	}
	if err := EnsurePrivateDirectory(p.JournalRoot, filepath.Dir(p.JournalRoot)); err != nil {
		return err
	}
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, err := uuid.Parse(entry.Name())
		if err != nil || id.String() != entry.Name() {
			continue
		}
		path, err := p.configPath(entry.Name())
		if err != nil {
			return err
		}
		current, mode, err := readPolicyFile(path, 64<<10)
		if err != nil {
			return err
		}
		record, err := p.record(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			held, changed, err := readOnlyRepositoryConfig(current)
			if err != nil {
				return err
			}
			if !changed {
				continue
			} // administrator's own read/none is untouched
			record = repositoryPolicyRecord{Schema: "filees.capacity-repository-policy/v1", RepoID: entry.Name(), Original: current, Held: held, Mode: uint32(mode)}
			raw, err := json.Marshal(record)
			if err != nil {
				return err
			}
			// Write-ahead backup: even a crash before the next rename is recoverable.
			if err := writePolicyFile(filepath.Join(p.JournalRoot, entry.Name()+".json"), raw, 0600); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if uint32(mode) != record.Mode {
			return errors.New("repository policy mode changed; administrator action required")
		}
		if bytes.Equal(current, record.Held) {
			continue
		}
		if !bytes.Equal(current, record.Original) || uint32(mode) != record.Mode {
			return fmt.Errorf("repository %s configuration changed during capacity stop", entry.Name())
		}
		if err := writePolicyFile(path, record.Held, mode); err != nil {
			return err
		}
	}
	return nil
}

func (p RepositoryPolicy) Restore() error {
	entries, err := os.ReadDir(p.JournalRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			return errors.New("unexpected repository policy journal entry")
		}
		repoID := strings.TrimSuffix(entry.Name(), ".json")
		path, err := p.configPath(repoID)
		if err != nil {
			return err
		}
		record, err := p.record(repoID)
		if err != nil {
			return err
		}
		current, mode, err := readPolicyFile(path, 64<<10)
		if err != nil {
			return err
		}
		if uint32(mode) != record.Mode {
			return errors.New("repository policy mode changed; administrator action required")
		}
		if !bytes.Equal(current, record.Original) {
			if !bytes.Equal(current, record.Held) {
				return errors.New("repository policy changed; administrator action required")
			}
			if err := writePolicyFile(path, record.Original, mode); err != nil {
				return err
			}
		}
		if err := os.Remove(filepath.Join(p.JournalRoot, entry.Name())); err != nil {
			return err
		}
		if err := durable.SyncDirectory(p.JournalRoot); err != nil {
			return err
		}
	}
	return nil
}
