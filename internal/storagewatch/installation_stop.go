package storagewatch

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
	"github.com/google/uuid"
)

var ErrInstallationStopped = errors.New("server admission stopped for a coordinated installation")

// InstallationBinding is local administrator input, not a client capability.
// Digests bind both complete manifests, NOT just the new worker executable.
// This building block does not validate signatures or authorize replacement.
type InstallationBinding struct {
	OperationID       string `json:"operation_id"`
	PreviousManifest  string `json:"previous_manifest_sha256"`
	CandidateManifest string `json:"candidate_manifest_sha256"`
}

type InstallationStop struct {
	Schema string `json:"schema"`
	InstallationBinding
	RequestedAt time.Time `json:"requested_at"`
}

func (b InstallationBinding) validate() error {
	id, err := uuid.Parse(b.OperationID)
	if err != nil || id == uuid.Nil || id.String() != b.OperationID {
		return errors.New("installation stop requires a canonical operation UUID")
	}
	for _, digest := range []string{b.PreviousManifest, b.CandidateManifest} {
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != digest {
			return errors.New("installation stop requires complete manifest SHA-256 bindings")
		}
	}
	return nil
}

func (g Gate) installationStopPath() string {
	return filepath.Join(g.Root, "installation-stop.json")
}

// InstallationStop is read-only. Absence is the legacy open state; malformed,
// aliased or unreadable metadata is NEVER treated as absence. No TTL or PID.
func (g Gate) InstallationStop() (*InstallationStop, error) {
	if !filepath.IsAbs(g.Root) || filepath.Clean(g.Root) == string(filepath.Separator) {
		return nil, errors.New("installation stop requires a dedicated absolute root")
	}
	root, err := os.Lstat(g.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // legacy bootstrap before creation of the activation root
	}
	if err != nil {
		return nil, err
	}
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("installation stop root must be a real directory")
	}
	info, err := os.Lstat(g.installationStopPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !privatePolicyMode(info.Mode()) || info.Size() > 4096 {
		return nil, errors.New("unsafe installation stop metadata")
	}
	f, err := os.Open(g.installationStopPath())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("installation stop identity changed")
	}
	var stop InstallationStop
	d := json.NewDecoder(io.LimitReader(f, 4097))
	d.DisallowUnknownFields()
	if err := d.Decode(&stop); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF || stop.Schema != "filees.installation-stop/v1" || stop.RequestedAt.IsZero() {
		return nil, errors.New("invalid installation stop")
	}
	if err := stop.InstallationBinding.validate(); err != nil {
		return nil, err
	}
	return &stop, nil
}

func (g Gate) CheckInstallationAdmission() error {
	stop, err := g.InstallationStop()
	if err != nil {
		return err
	}
	if stop != nil {
		return ErrInstallationStopped
	}
	return nil
}

// RequestInstallationStop persists intent BEFORE waiting for writers/sessions.
// A failed drain, cancel or process death never clears it. No release method is
// provided until compatible backup receipts and whole-set recovery exist.
// The stable admission lock is separate from the writer lock: an active writer
// must not delay publication of the stop that refuses its successors.
func (g Gate) RequestInstallationStop(ctx context.Context, binding InstallationBinding) error {
	if err := binding.validate(); err != nil {
		return err
	}
	if _, err := g.InstallationStop(); err != nil {
		return err
	}
	return g.withNamedLock(ctx, ".installation-stop.lock", true, func() error {
		stop, err := g.InstallationStop()
		if err != nil {
			return err
		}
		if stop != nil {
			if stop.InstallationBinding != binding {
				return errors.New("another installation binding is pending")
			}
			// Also acknowledge a prior rename whose directory sync failed.
			return durable.SyncDirectory(g.Root)
		}
		raw, err := json.Marshal(InstallationStop{Schema: "filees.installation-stop/v1", InstallationBinding: binding, RequestedAt: time.Now().UTC()})
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(g.Root, ".installation-stop-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if err := preservePolicyOwner(f, g.Root); err != nil {
			return err
		}
		if _, err := f.Write(raw); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Rename(f.Name(), g.installationStopPath()); err != nil {
			return err
		}
		return durable.SyncDirectory(g.Root)
	})
}

// WithInstallationWritersStopped proves cooperative writer completion only
// while run holds the lease. It does NOT acknowledge resident-service drain.
// An old persisted "drained" flag would be unsafe after a process restart;
// instead every recovery must reacquire real exclusion and session barriers.
func (g Gate) WithInstallationWritersStopped(ctx context.Context, binding InstallationBinding, run func() error) error {
	if err := binding.validate(); err != nil {
		return err
	}
	if run == nil {
		return errors.New("installation writer drain requires an operation")
	}
	return g.WithExclusive(ctx, func() error {
		stop, err := g.InstallationStop()
		if err != nil {
			return err
		}
		if stop == nil || stop.InstallationBinding != binding {
			return errors.New("installation stop binding not present")
		}
		return run()
	})
}
