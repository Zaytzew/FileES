package commit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"filees/pkg/cloudfiles"
	"filees/pkg/errcat"
	"filees/pkg/portablepath"
)

// These receipts never authorize replacement of user bytes. A verified initial
// delivery permits subsequent edits; pending/held evidence survives every build.
type AnchorIntegrityRecord struct {
	Generation string `json:"generation"`
	Schema     string `json:"schema"`
	Path       string `json:"path"`
	URL        string `json:"url"`
	Identity   string `json:"identity"`
	FileID     string `json:"file_id"`
	Revision   int64  `json:"revision"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Phase      string `json:"phase"`
	Reason     string `json:"reason,omitempty"`
}

var anchorIntegrityMu sync.Mutex

func AnchorIntegrityError(rel string, cause error) error {
	return errcat.New("sync.download_integrity_held", map[string]string{"path": rel}, cause)
}

func integrityDir(wc string, create bool) (string, error) {
	current := wc
	for _, part := range []string{".filees", "state", "anchor-integrity"} {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if create && errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe integrity directory")
		}
	}
	return current, nil
}

func integrityName(rel string) string {
	sum := sha256.Sum256([]byte(portablepath.CollisionKey(rel)))
	return hex.EncodeToString(sum[:]) + ".json"
}

func validIntegrity(r AnchorIntegrityRecord) bool {
	hash, err := hex.DecodeString(r.SHA256)
	_, generationErr := uuid.Parse(r.Generation)
	return generationErr == nil && r.Schema == "filees.anchor-integrity/v1" && safeIntentPath(r.Path) && r.URL != "" &&
		r.Identity != "" && r.FileID != "" && r.Revision > 0 && r.Size >= 0 && ((err == nil && len(hash) == 32) || (r.SHA256 == "" && r.Phase != "verified")) &&
		(r.Phase == "pending" || r.Phase == "held" || r.Phase == "verified")
}

func readIntegrityFile(path string) (AnchorIntegrityRecord, error) {
	var r AnchorIntegrityRecord
	info, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 16384 {
		return r, errors.New("unsafe integrity receipt")
	}
	file, err := os.Open(path)
	if err != nil {
		return r, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return r, errors.New("integrity receipt replaced")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil {
		return r, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &r) != nil || !validIntegrity(r) {
		return r, errors.New("invalid integrity receipt")
	}
	if filepath.Base(path) != integrityName(r.Path) {
		return r, errors.New("misbound integrity receipt")
	}
	return r, nil
}

func ReadAnchorIntegrity(wc, rel string) (AnchorIntegrityRecord, error) {
	if !safeIntentPath(rel) {
		return AnchorIntegrityRecord{}, errors.New("invalid integrity path")
	}
	dir, err := integrityDir(wc, false)
	if err != nil {
		return AnchorIntegrityRecord{}, err
	}
	return readIntegrityFile(filepath.Join(dir, integrityName(rel)))
}

// Begin cannot erase an unexplained difference or a proof for a different
// object. Repeated in-flight reads of the same object share their reference.
func BeginAnchorIntegrity(wc string, record *AnchorIntegrityRecord, partial bool) error {
	anchorIntegrityMu.Lock()
	defer anchorIntegrityMu.Unlock()
	if record == nil {
		return errors.New("missing integrity binding")
	}
	r := *record
	r.Generation = uuid.NewString()
	r.Schema = "filees.anchor-integrity/v1"
	r.Phase = "pending"
	r.Reason = ""
	if !validIntegrity(r) {
		return errors.New("invalid integrity binding")
	}
	previous, err := ReadAnchorIntegrity(wc, r.Path)
	if err == nil {
		same := sameIntegrityBinding(previous, r)
		if previous.Phase == "held" && !same {
			return AnchorIntegrityError(r.Path, errors.New("previous integrity hold needs inspection"))
		}
		if same && (previous.Phase != "verified" || !partial) {
			*record = previous
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return AnchorIntegrityError(r.Path, err)
	}
	dir, err := integrityDir(wc, true)
	if err != nil {
		return err
	}
	if err := atomicWriteJSONSliceInExistingDir(filepath.Join(dir, integrityName(r.Path)), r); err != nil {
		return err
	}
	*record = r
	return nil
}

func sameIntegrityBinding(a, b AnchorIntegrityRecord) bool {
	a.Generation = ""
	b.Generation = ""
	a.Schema = ""
	a.Phase = ""
	a.Reason = ""
	b.Schema = ""
	b.Phase = ""
	b.Reason = ""
	return a == b
}

// Only the coordinator's verified native read-back may advance to verified.
// A stale completion can never bless a different object/reference.
func FinishAnchorIntegrity(wc string, r AnchorIntegrityRecord, verified bool, reason string) error {
	anchorIntegrityMu.Lock()
	defer anchorIntegrityMu.Unlock()
	previous, err := ReadAnchorIntegrity(wc, r.Path)
	if err != nil {
		return AnchorIntegrityError(r.Path, err)
	}
	if previous.Generation != r.Generation || !sameIntegrityBinding(previous, r) {
		return AnchorIntegrityError(r.Path, errors.New("integrity binding changed"))
	}
	previous.Phase = "held"
	previous.Reason = reason
	if verified {
		previous.Phase = "verified"
		previous.Reason = ""
	}
	if !validIntegrity(previous) {
		return AnchorIntegrityError(r.Path, errors.New("verification lacks a reference hash"))
	}
	dir, err := integrityDir(wc, false)
	if err != nil {
		return err
	}
	return atomicWriteJSONSliceInExistingDir(filepath.Join(dir, integrityName(r.Path)), previous)
}

// Used by all publication variants, including nocfapi and restart recovery.
// A held descendant also fences a directory move/delete; unrelated paths pass.
func CheckAnchorIntegrity(wc string, paths []string) error {
	dir, err := integrityDir(wc, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return AnchorIntegrityError("", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return AnchorIntegrityError("", err)
	}
	if len(entries) > 20000 {
		return AnchorIntegrityError("", errors.New("integrity receipt limit"))
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".filees-cache-") && strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		r, err := readIntegrityFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return AnchorIntegrityError("", err)
		}
		if r.Phase == "verified" {
			continue
		}
		for _, p := range paths {
			a, b := portablepath.CollisionKey(p), portablepath.CollisionKey(r.Path)
			if p == "." || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return AnchorIntegrityError(r.Path, fmt.Errorf("%s: %s", r.Phase, r.Reason))
			}
		}
	}
	return nil
}

func (s *Service) checkDownloadIntegrity(wc string, paths []string) error {
	if err := CheckAnchorIntegrity(wc, paths); err != nil {
		return err
	}
	if !s.Anchor {
		return nil
	}
	for _, rel := range paths {
		if !safeIntentPath(rel) {
			return AnchorIntegrityError(rel, errors.New("invalid publication path"))
		}
		path := filepath.Join(wc, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			continue
		}
		if cloudfiles.IsPlaceholder(path) {
			local, _ := cloudfiles.LocalState(path)
			if !local {
				return AnchorIntegrityError(rel, errors.New("remote placeholder has no completed delivery proof"))
			}
		}
	}
	return nil
}
