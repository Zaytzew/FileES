package localrepo

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"filees/internal/durable"
	"github.com/google/uuid"
)

// Conversion journals are separate from the moved WC and from its lifecycle
// record. They record effects; they do not grant authority, quiesce a writer or
// authorize deletion. The coordinator must revalidate the current attachment
// under the existing local-intent fence before every filesystem effect.
const conversionSchema = "filees.wc-conversion/v1"

type ConversionPhase string

const (
	ConversionWaiting    ConversionPhase = "waiting"
	ConversionRetaining  ConversionPhase = "retaining"
	ConversionRetained   ConversionPhase = "retained"
	ConversionCreating   ConversionPhase = "creating"
	ConversionPreserving ConversionPhase = "preserving"
	ConversionVerifying  ConversionPhase = "verifying"
	ConversionCleanup    ConversionPhase = "cleanup"
	ConversionComplete   ConversionPhase = "complete"
	ConversionRolledBack ConversionPhase = "rolled_back"
	ConversionCanceled   ConversionPhase = "canceled"
)

// ConversionRecord is a value receipt: callers cannot mutate the journal by
// changing a returned slice or pointer. FileIDs are opaque native identities.
// Nothing in this journal is a credential or permission to read the server.
type ConversionRecord struct {
	Schema       string          `json:"schema"`
	ID           string          `json:"id"`
	AttachmentID string          `json:"attachment_id"`
	ServerID     string          `json:"server_id"`
	RepoID       string          `json:"repo_id"`
	RepoURL      string          `json:"repo_url"`
	SourcePath   string          `json:"source_path"`
	BackupPath   string          `json:"backup_path"`
	Phase        ConversionPhase `json:"phase"`
	SourceFileID string          `json:"source_file_id,omitempty"`
	Prepared     bool            `json:"prepared,omitempty"`
	Revision     int64           `json:"revision"` // meaningful only with Prepared; r0 is valid
	PlanPath     string          `json:"plan_path,omitempty"`
	PlanSHA256   string          `json:"plan_sha256,omitempty"`
	LocalObjects int64           `json:"local_objects"`
	PointFileID  string          `json:"point_file_id,omitempty"`
	ProviderID   string          `json:"provider_id,omitempty"`
	Blocked      string          `json:"blocked,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type ConversionJournal struct {
	mu     sync.Mutex
	path   string
	record ConversionRecord
}

// CreateConversionJournal exclusively creates an intent; it never overwrites
// an existing journal. It does not touch either working-copy path. A partial
// initial write fails closed on Open rather than inventing a completed stage.
// Like Store, it has one daemon-owned writer instance; Open is for recovery,
// not for obtaining a second concurrently writing handle to the same journal.
func CreateConversionJournal(path string, attachment Record) (*ConversionJournal, error) {
	if attachment.State != StateAttached || attachment.Anchor || attachment.Sparse || attachment.Purpose != "" ||
		attachment.RelocationQueued || attachment.PendingLocalPath != "" || attachment.RemoteDeletionObserved || attachment.LastError != "" {
		return nil, errors.New("conversion requires an ordinary attached full working copy")
	}
	if err := validate(attachment); err != nil {
		return nil, err
	}
	root := filepath.Clean(attachment.LocalPath)
	now := time.Now().UTC()
	r := ConversionRecord{Schema: conversionSchema, ID: uuid.NewString(), AttachmentID: attachment.OperationID,
		ServerID: attachment.ServerID, RepoID: attachment.RepoID, RepoURL: attachment.RepoURL,
		SourcePath: root, BackupPath: filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+"-converting"),
		Phase: ConversionWaiting, CreatedAt: now, UpdatedAt: now}
	j := &ConversionJournal{path: path, record: r}
	if err := j.validateLocation(); err != nil {
		return nil, err
	}
	if err := validateConversion(r); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err := writeConversion(f, r); err != nil {
		return nil, err
	}
	if err := durable.SyncDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return j, nil
}

func OpenConversionJournal(path string) (*ConversionJournal, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("conversion journal is not a bounded regular file")
	}
	dec := json.NewDecoder(io.LimitReader(f, 64<<10))
	dec.DisallowUnknownFields()
	var record ConversionRecord
	if err := dec.Decode(&record); err != nil {
		return nil, fmt.Errorf("decode conversion journal: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("conversion journal has trailing data")
	}
	j := &ConversionJournal{path: path, record: record}
	if err := j.validateLocation(); err != nil {
		return nil, err
	}
	if err := validateConversion(record); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *ConversionJournal) Get() ConversionRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.record
}

// Prepare is recorded after quiesce/clean inspection, before the rename. The
// private immutable plan and the actual source FileID must already be durable.
// A restart in retaining must inspect paths/identity before attempting rename.
func (j *ConversionJournal) Prepare(expected ConversionRecord, sourceFileID string, revision int64, planPath, digest string, localObjects int64) (ConversionRecord, error) {
	return j.change(expected, ConversionWaiting, func(r *ConversionRecord) {
		r.Phase, r.Prepared, r.SourceFileID = ConversionRetaining, true, sourceFileID
		r.Revision, r.PlanPath, r.PlanSHA256, r.LocalObjects = revision, planPath, digest, localObjects
	})
}

// These receipts are written only AFTER the named effect has been verified.
// This API deliberately performs no rename, checkout, unregister or cleanup.
func (j *ConversionJournal) MarkRetained(expected ConversionRecord) (ConversionRecord, error) {
	return j.step(expected, ConversionRetaining, ConversionRetained)
}

func (j *ConversionJournal) BeginPoint(expected ConversionRecord) (ConversionRecord, error) {
	return j.step(expected, ConversionRetained, ConversionCreating)
}

func (j *ConversionJournal) MarkPointReady(expected ConversionRecord, pointFileID, providerID string) (ConversionRecord, error) {
	return j.change(expected, ConversionCreating, func(r *ConversionRecord) {
		r.Phase, r.PointFileID, r.ProviderID = ConversionPreserving, pointFileID, providerID
	})
}

func (j *ConversionJournal) MarkPreserved(expected ConversionRecord) (ConversionRecord, error) {
	return j.step(expected, ConversionPreserving, ConversionVerifying)
}

func (j *ConversionJournal) MarkVerified(expected ConversionRecord) (ConversionRecord, error) {
	return j.step(expected, ConversionVerifying, ConversionCleanup)
}

func (j *ConversionJournal) MarkComplete(expected ConversionRecord) (ConversionRecord, error) {
	return j.step(expected, ConversionCleanup, ConversionComplete)
}

// Cancel is safe only before preparation; later cancellation requires a
// verified filesystem rollback, not just discarding the forward receipt.
func (j *ConversionJournal) Cancel(expected ConversionRecord) (ConversionRecord, error) {
	return j.change(expected, ConversionWaiting, func(r *ConversionRecord) { r.Phase = ConversionCanceled; r.Blocked = "" })
}

// MarkRolledBack is a receipt, not a request to restore over an occupied root.
// The coordinator must first prove the restored original FileID, preservation
// of every local object, and absence of an unaccounted-for new point.
func (j *ConversionJournal) MarkRolledBack(expected ConversionRecord, restoredFileID string) (ConversionRecord, error) {
	if !expected.Prepared || restoredFileID != expected.SourceFileID || expected.Phase == ConversionCleanup || conversionTerminal(expected.Phase) {
		return ConversionRecord{}, errors.New("conversion rollback has no matching source receipt")
	}
	return j.change(expected, expected.Phase, func(r *ConversionRecord) { r.Phase = ConversionRolledBack; r.Blocked = "" })
}

// Block preserves the last effect boundary. Retry must be explicit; a process
// restart alone must not replay a known failed filesystem operation.
func (j *ConversionJournal) Block(expected ConversionRecord, cause error) (ConversionRecord, error) {
	if cause == nil || strings.TrimSpace(cause.Error()) == "" || conversionTerminal(expected.Phase) {
		return ConversionRecord{}, errors.New("conversion requires a concrete failure")
	}
	return j.change(expected, expected.Phase, func(r *ConversionRecord) { r.Blocked = cause.Error() })
}

func (j *ConversionJournal) Retry(expected ConversionRecord) (ConversionRecord, error) {
	if expected.Blocked == "" || conversionTerminal(expected.Phase) {
		return ConversionRecord{}, errors.New("conversion is not blocked")
	}
	return j.change(expected, expected.Phase, func(r *ConversionRecord) { r.Blocked = "" })
}

func (j *ConversionJournal) step(expected ConversionRecord, from, to ConversionPhase) (ConversionRecord, error) {
	return j.change(expected, from, func(r *ConversionRecord) { r.Phase = to })
}

func (j *ConversionJournal) change(expected ConversionRecord, from ConversionPhase, fn func(*ConversionRecord)) (ConversionRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.record != expected || j.record.Phase != from {
		return ConversionRecord{}, errors.New("conversion receipt changed")
	}
	next := j.record
	fn(&next)
	if j.record.Blocked != "" && next.Blocked != "" && next.Phase != j.record.Phase {
		return ConversionRecord{}, errors.New("conversion is blocked; explicit retry is required")
	}
	next.UpdatedAt = time.Now().UTC()
	if err := validateConversion(next); err != nil {
		return ConversionRecord{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(j.path), ".conversion-*.tmp")
	if err != nil {
		return ConversionRecord{}, err
	}
	defer os.Remove(f.Name())
	if err := writeConversion(f, next); err != nil {
		return ConversionRecord{}, err
	}
	if err := os.Rename(f.Name(), j.path); err != nil {
		return ConversionRecord{}, err
	}
	// Publish the same receipt in memory even if directory fsync fails after
	// rename: callers must stop on the error and reload, not repeat an old CAS.
	j.record = next
	if err := durable.SyncDirectory(filepath.Dir(j.path)); err != nil {
		return ConversionRecord{}, err
	}
	return next, nil
}

func writeConversion(f *os.File, r ConversionRecord) error {
	err := f.Chmod(0600)
	if err == nil {
		err = json.NewEncoder(f).Encode(r)
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func (j *ConversionJournal) validateLocation() error {
	if !filepath.IsAbs(j.path) || filepath.Clean(j.path) != j.path ||
		conversionPathsOverlap(j.path, j.record.SourcePath) || conversionPathsOverlap(j.path, j.record.BackupPath) {
		return errors.New("conversion journal must be outside both working-copy paths")
	}
	return nil
}

func conversionPathsOverlap(a, b string) bool {
	if runtime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	return pathsOverlap(a, b)
}

func conversionTerminal(phase ConversionPhase) bool {
	return phase == ConversionComplete || phase == ConversionRolledBack || phase == ConversionCanceled
}

func validateConversion(r ConversionRecord) error {
	if r.Schema != conversionSchema || uuid.Validate(r.ID) != nil || uuid.Validate(r.AttachmentID) != nil ||
		strings.TrimSpace(r.ServerID) == "" || strings.TrimSpace(r.RepoID) == "" ||
		strings.ContainsAny(r.ServerID+r.RepoID, "/\\\x00\r\n\t ") || !strings.HasPrefix(r.RepoURL, "svn+ssh://") {
		return errors.New("invalid conversion attachment binding")
	}
	root := filepath.Clean(r.SourcePath)
	if !filepath.IsAbs(root) || root != r.SourcePath || filepath.Dir(root) == root ||
		r.BackupPath != filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+"-converting") {
		return errors.New("invalid conversion recovery path")
	}
	phase := map[ConversionPhase]int{ConversionWaiting: 0, ConversionRetaining: 1, ConversionRetained: 2,
		ConversionCreating: 3, ConversionPreserving: 4, ConversionVerifying: 5, ConversionCleanup: 6, ConversionComplete: 7, ConversionRolledBack: 8, ConversionCanceled: 0}
	n, ok := phase[r.Phase]
	if !ok || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || r.LocalObjects < 0 || len(r.Blocked) > 16384 {
		return errors.New("invalid conversion stage")
	}
	if n == 0 {
		if r.Prepared || r.SourceFileID != "" || r.Revision != 0 || r.PlanPath != "" || r.PlanSHA256 != "" || r.LocalObjects != 0 {
			return errors.New("unprepared conversion carries a snapshot receipt")
		}
	} else {
		digest, err := hex.DecodeString(r.PlanSHA256)
		if !r.Prepared || r.SourceFileID == "" || r.Revision < 0 || err != nil || len(digest) != 32 ||
			!filepath.IsAbs(r.PlanPath) || filepath.Clean(r.PlanPath) != r.PlanPath ||
			conversionPathsOverlap(r.PlanPath, root) || conversionPathsOverlap(r.PlanPath, r.BackupPath) {
			return errors.New("conversion is missing its prepared snapshot")
		}
	}
	if n >= 4 && r.Phase != ConversionRolledBack {
		if r.PointFileID == "" || r.PointFileID == r.SourceFileID || r.ProviderID == "" {
			return errors.New("conversion is missing its distinct point receipt")
		}
	} else if r.Phase != ConversionRolledBack && (r.PointFileID != "" || r.ProviderID != "") {
		return errors.New("conversion carries a point receipt before point creation")
	}
	if conversionTerminal(r.Phase) && r.Blocked != "" {
		return errors.New("terminal conversion cannot remain blocked")
	}
	return nil
}
