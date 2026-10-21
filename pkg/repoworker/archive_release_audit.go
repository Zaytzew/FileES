package repoworker

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
	"time"

	"github.com/google/uuid"
)

const archiveReleaseAuditSchema = "filees.archive-admin-release/v1"

// ArchiveReleaseAudit is immutable canonical authority, not a client receipt.
// Previous must be an archived policy without another embedded Release; old
// administrative events remain separate versioned records after refreeze.
type ArchiveReleaseAudit struct {
	Schema         string                  `json:"schema"`
	RepoID         string                  `json:"repo_id"`
	OperationID    string                  `json:"operation_id"`
	Actor          string                  `json:"actor"`
	Reason         string                  `json:"reason"`
	RequestedAt    time.Time               `json:"requested_at"`
	Previous       RepositoryArchivePolicy `json:"previous"`
	PreviousSHA256 string                  `json:"previous_sha256"`
	RuntimeBefore  ArchiveProtectionRecord `json:"runtime_before"`
}

func archivePolicyDigest(p RepositoryArchivePolicy) string {
	raw, _ := json.MarshalIndent(p, "", "  ")
	digest := sha256.Sum256(append(raw, '\n'))
	return hex.EncodeToString(digest[:])
}

func archiveReleaseText(text string, limit int) bool {
	return len(text) <= limit && strings.TrimSpace(text) != "" && !strings.ContainsAny(text, "\x00\r\n")
}

func (a ArchiveReleaseAudit) validatePolicy(p RepositoryArchivePolicy) error {
	if a.Schema != archiveReleaseAuditSchema || a.RequestedAt.IsZero() || !archiveReleaseText(a.Actor, 128) || !archiveReleaseText(a.Reason, 1024) {
		return errors.New("invalid canonical administrative release audit")
	}
	for _, id := range []string{a.RepoID, a.OperationID} {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return errors.New("invalid release audit identity")
		}
	}
	old := a.Previous
	if old.Release != nil || old.State != "archived" || old.validate() != nil || old.Epoch == ^uint64(0) || a.OperationID == old.OperationID || a.PreviousSHA256 != archivePolicyDigest(old) {
		return errors.New("release audit does not bind an archived policy")
	}
	r := a.RuntimeBefore
	if r.validate() != nil || r.State != "archived" || r.RepoID != a.RepoID || r.Epoch != old.Epoch || r.OperationID != old.OperationID || r.RepositoryUUID != old.RepositoryUUID || r.Revision != old.Revision {
		return errors.New("release audit runtime binding is invalid")
	}
	if p.OperationID != a.OperationID || p.Epoch != old.Epoch+1 || p.RepositoryUUID != old.RepositoryUUID || p.Revision != old.Revision || !p.StartedAt.Equal(a.RequestedAt) {
		return errors.New("release policy differs from its administrative audit")
	}
	return nil
}

func canonicalArchiveReleasePath(root, repo string, epoch uint64) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("archive audit requires absolute authority working copy")
	}
	if _, err := repositoryRecordPath(root, repo); err != nil {
		return "", err
	}
	return filepath.Join(root, "admin", "repository-policy-audit", repo, fmt.Sprintf("%020d-release.json", epoch)), nil
}

func readCanonicalArchiveRelease(path string) (ArchiveReleaseAudit, error) {
	var audit ArchiveReleaseAudit
	info, err := os.Lstat(path)
	if err != nil {
		return audit, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16384 {
		return audit, errors.New("unsafe canonical archive audit file")
	}
	f, err := os.Open(path)
	if err != nil {
		return audit, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&audit); err != nil {
		return audit, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return audit, errors.New("trailing canonical archive audit data")
	}
	return audit, nil
}

func verifyCanonicalArchiveRelease(root, repo string, audit ArchiveReleaseAudit) error {
	if audit.RepoID != repo {
		return errors.New("canonical archive audit belongs to another repository")
	}
	path, err := canonicalArchiveReleasePath(root, repo, audit.Previous.Epoch+1)
	if err != nil {
		return err
	}
	got, err := readCanonicalArchiveRelease(path)
	if err != nil {
		return err
	}
	if got != audit {
		return errors.New("canonical archive audit is missing or inconsistent")
	}
	return nil
}

func writeCanonicalArchiveRelease(root, repo string, audit ArchiveReleaseAudit) (string, error) {
	if audit.RepoID != repo {
		return "", errors.New("canonical archive audit repository changed")
	}
	path, err := canonicalArchiveReleasePath(root, repo, audit.Previous.Epoch+1)
	if err != nil {
		return "", err
	}
	if got, err := readCanonicalArchiveRelease(path); err == nil {
		if got != audit {
			return "", errors.New("refusing to overwrite a canonical archive audit")
		}
		return path, syncCanonicalArchiveAudit(filepath.Dir(path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".archive-audit-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return "", err
	}
	if err := os.Link(f.Name(), path); err != nil {
		// A conflicting existing record is never overwritten.
		existing, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(existing, raw) {
			return "", err
		}
	}
	if err := syncCanonicalArchiveAudit(filepath.Dir(path)); err != nil {
		return "", err
	}
	return path, nil
}

func syncCanonicalArchiveAudit(dir string) error {
	// Include both newly created directory entries beneath the existing
	// canonical admin root, also when retrying a previous fsync failure.
	for i := 0; i < 3; i++ {
		if err := syncDirectory(dir); err != nil {
			return err
		}
		dir = filepath.Dir(dir)
	}
	return nil
}
