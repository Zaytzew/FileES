package svnrotate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A prepared swap is immutable. Recovery derives progress from UUIDs and
// artifacts, never from a phase flag that could lag behind a rename.
type swapRecord struct {
	Schema    string            `json:"schema"`
	Repo      string            `json:"repo"`
	Work      string            `json:"work"`
	Meta      Meta              `json:"meta"`
	Artifacts map[string]string `json:"artifacts"`
}

// Tests replace this in a child process to terminate at durable boundaries.
var swapCheckpoint = func(string) {}

func swapPath(repo, archive string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repo)))
	return filepath.Join(archive, fmt.Sprintf(".swap-%x.json", sum))
}

// Recover completes a prepared generation replacement, including its archive.
// A nonempty reason restricts recovery to that operation. No journal means no
// mutation. Callers must authorize the repository before entering this API.
func Recover(repo, archive, reason string) (Meta, bool, error) {
	if !filepath.IsAbs(repo) || !filepath.IsAbs(archive) || filepath.Clean(repo) == filepath.Clean(archive) {
		return Meta{}, false, errors.New("invalid recovery paths")
	}
	if _, err := os.Lstat(swapPath(repo, archive)); errors.Is(err, os.ErrNotExist) {
		return Meta{}, false, nil
	} else if err != nil {
		return Meta{}, false, err
	}
	release, err := acquireLock(filepath.Join(archive, ".rotate.lock"))
	if err != nil {
		return Meta{}, false, err
	}
	defer release()
	return recoverSwap(repo, archive, reason)
}

func recoverSwap(repo, archive, reason string) (Meta, bool, error) {
	name := swapPath(repo, archive)
	st, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, false, nil
	}
	if err != nil {
		return Meta{}, true, err
	}
	if !st.Mode().IsRegular() || st.Size() > 64<<10 {
		return Meta{}, true, errors.New("invalid swap journal file")
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return Meta{}, true, err
	}
	var record swapRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return Meta{}, true, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Meta{}, true, errors.New("trailing swap journal data")
	}
	if err := record.validate(repo, archive); err != nil {
		return Meta{}, true, err
	}
	if reason != "" && record.Meta.Reason != reason {
		return Meta{}, true, errors.New("prepared swap belongs to another operation")
	}
	if err := record.finish(archive); err != nil {
		return Meta{}, true, err
	}
	return record.Meta, true, nil
}

func (r swapRecord) validate(repo, archive string) error {
	m := r.Meta
	if r.Schema != "filees.svn-swap/v1" || r.Repo != filepath.Clean(repo) || filepath.Dir(r.Work) != filepath.Clean(archive) ||
		!(strings.HasPrefix(filepath.Base(r.Work), ".load-work-") || strings.HasPrefix(filepath.Base(r.Work), ".rotate-work-")) ||
		m.Tag == "" || m.Tag == "." || m.Tag == ".." || filepath.Base(m.Tag) != m.Tag || m.ArchiveDir != filepath.Join(archive, m.Tag+".svn") ||
		m.OldUUID == "" || m.NewUUID == "" || m.NewUUID == m.OldUUID || len(r.Artifacts) < 1 || len(r.Artifacts) > 2 {
		return errors.New("invalid swap journal identity")
	}
	if _, ok := r.Artifacts[m.Tag+".log.xml"]; !ok {
		return errors.New("swap journal lacks manifest")
	}
	for name, digest := range r.Artifacts {
		if filepath.Base(name) != name || !(name == m.Tag+".log.xml" || strings.HasPrefix(name, m.Tag+".r") && strings.HasSuffix(name, ".dump.gz")) {
			return errors.New("invalid swap artifact name")
		}
		if b, err := hex.DecodeString(digest); err != nil || len(b) != sha256.Size {
			return errors.New("invalid swap artifact hash")
		}
	}
	return nil
}

func prepareSwap(repo, archive, work string, meta Meta, names ...string) (swapRecord, error) {
	r := swapRecord{Schema: "filees.svn-swap/v1", Repo: repo, Work: work, Meta: meta, Artifacts: map[string]string{}}
	for _, name := range names {
		if name != "" {
			digest, err := artifactHash(filepath.Join(work, name))
			if err != nil {
				return r, err
			}
			r.Artifacts[name] = digest
		}
	}
	if err := r.validate(repo, archive); err != nil {
		return r, err
	}
	if err := fsyncDir(work); err != nil {
		return r, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	if err := publishBytes(swapPath(repo, archive), raw, 0600); err != nil {
		return r, err
	}
	return r, fsyncDir(archive)
}

func uuidIfPresent(path string) (string, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("generation is not a plain directory: %s", path)
	}
	return repoUUID(path)
}

func (r swapRecord) finish(archive string) error {
	// Refuse foreign generations before changing anything.
	hot, err := uuidIfPresent(r.Repo)
	if err != nil {
		return err
	}
	old, err := uuidIfPresent(r.Meta.ArchiveDir)
	if err != nil {
		return err
	}
	if st, err := os.Lstat(r.Work); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
		return errors.New("swap staging directory is not plain")
	}
	staged, err := uuidIfPresent(filepath.Join(r.Work, "new.svn"))
	if err != nil {
		return err
	}
	if !(hot == r.Meta.OldUUID && old == "" && staged == r.Meta.NewUUID || hot == "" && old == r.Meta.OldUUID && staged == r.Meta.NewUUID || hot == r.Meta.NewUUID && old == r.Meta.OldUUID && staged == "") {
		return errors.New("swap generation identities conflict; no recovery mutation performed")
	}
	for name, hash := range r.Artifacts {
		if err := checkArtifactSource(r.Work, archive, name, hash); err != nil {
			return err
		}
	}
	if hot == r.Meta.OldUUID {
		if err := os.Rename(r.Repo, r.Meta.ArchiveDir); err != nil {
			return err
		}
		if err := syncSwapDirs(r.Repo, archive, r.Work); err != nil {
			return err
		}
		swapCheckpoint("archived")
		hot = ""
	}
	if hot == "" {
		if err := os.Rename(filepath.Join(r.Work, "new.svn"), r.Repo); err != nil {
			return err
		}
		if err := syncSwapDirs(r.Repo, archive, r.Work); err != nil {
			return err
		}
		swapCheckpoint("installed")
	}
	for name, hash := range r.Artifacts {
		target := filepath.Join(archive, name)
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			if err := os.Link(filepath.Join(r.Work, name), target); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		got, err := artifactHash(target)
		if err != nil {
			return err
		}
		if got != hash {
			return errors.New("archive artifact changed")
		}
	}
	if err := fsyncDir(archive); err != nil {
		return err
	}
	swapCheckpoint("artifacts")
	raw, err := json.MarshalIndent(r.Meta, "", "  ")
	if err != nil {
		return err
	}
	if err := publishBytes(filepath.Join(archive, r.Meta.Tag+".meta.json"), append(raw, '\n'), 0640); err != nil {
		return err
	}
	swapCheckpoint("metadata")
	frozen := fmt.Sprintf("Frozen FileES repository generation.\nTag: %s\nUUID: %s\nHead: r%d\nCommits are permanently blocked by hooks/pre-commit.\n", r.Meta.Tag, r.Meta.OldUUID, r.Meta.OldHead)
	if err := publishBytes(filepath.Join(r.Meta.ArchiveDir, "FROZEN"), []byte(frozen), 0444); err != nil {
		return err
	}
	if err := fsyncDir(r.Meta.ArchiveDir); err != nil {
		return err
	}
	if err := fsyncDir(archive); err != nil {
		return err
	}
	swapCheckpoint("frozen")
	// Keep the journal until staging cleanup finishes. A restart after cleanup
	// proves completion using the installed UUIDs and archived artifacts.
	if err := os.RemoveAll(r.Work); err != nil {
		return err
	}
	swapCheckpoint("cleaned")
	if err := os.Remove(swapPath(r.Repo, archive)); err != nil {
		return err
	}
	if err := fsyncDir(archive); err != nil {
		return err
	}
	return nil
}

func syncSwapDirs(repo, archive, work string) error {
	return errors.Join(fsyncDir(filepath.Dir(repo)), fsyncDir(archive), fsyncDir(work))
}
func artifactHash(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("artifact is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func checkArtifactSource(work, archive, name, want string) error {
	for _, root := range []string{work, archive} {
		got, err := artifactHash(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if got != want {
			return errors.New("swap artifact hash mismatch")
		}
		return nil
	}
	return errors.New("swap artifact is missing")
}

// Publish a fully written inode without ever overwriting an existing artifact.
// A crash leaves either no target or the complete target, never partial JSON.
func publishBytes(path string, raw []byte, mode os.FileMode) error {
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() {
			return errors.New("artifact target is not a regular file")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, raw) {
			return errors.New("existing artifact differs")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".swap-artifact-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Link(name, path)
}
