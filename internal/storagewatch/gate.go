package storagewatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
)

var ErrWriteHeld = errors.New("server writes are held for storage recovery")

// Gate lives in the existing, private activation root. Unlike an observation,
// its stop survives deletion of optional monitor settings and has no TTL.
// The stable lock inode is never replaced along with the policy JSON.
type Gate struct{ Root string }

// Prepare creates the stable lease inode and private acknowledgement directory
// before a resident service unveils the exact gate paths. It admits no effects.
func (g Gate) Prepare(ctx context.Context) error {
	return g.withLock(ctx, false, func() error {
		return EnsurePrivateDirectory(g.writerDirectory(), g.Root)
	})
}

type gatePolicy struct {
	Schema    string    `json:"schema"`
	Phase     string    `json:"phase"`
	ChangedAt time.Time `json:"changed_at"`
}

func (g Gate) Held() (bool, error) {
	phase, err := g.Phase()
	return phase != "open", err
}

func (g Gate) Phase() (string, error) {
	if !filepath.IsAbs(g.Root) {
		return "draining", errors.New("write gate root must be absolute")
	}
	path := filepath.Join(g.Root, "capacity-write-policy.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "open", nil
	} // unconfigured legacy server
	if err != nil {
		return "draining", err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 || !privatePolicyMode(info.Mode()) {
		return "draining", errors.New("unsafe capacity write policy")
	}
	f, err := os.Open(path)
	if err != nil {
		return "draining", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "draining", errors.New("capacity write policy changed during inspection")
	}
	var policy gatePolicy
	d := json.NewDecoder(io.LimitReader(f, 4097))
	d.DisallowUnknownFields()
	if err := d.Decode(&policy); err != nil {
		return "draining", err
	}
	if d.Decode(new(any)) != io.EOF || policy.Schema != "filees.capacity-write-policy/v1" || policy.ChangedAt.IsZero() || (policy.Phase != "open" && policy.Phase != "draining" && policy.Phase != "held") {
		return "draining", errors.New("invalid capacity write policy")
	}
	return policy.Phase, nil
}

// Hold is published BEFORE draining writers, so new admissions fail closed.
// An error is never proof that a write stop was durably installed.
func (g Gate) Hold() error { return g.set("draining") }

func (g Gate) AcknowledgeHeld() error { return g.set("held") }

// Resume is called only inside WithExclusive, after a fresh recovery sample,
// repository-policy restoration and durable observation have all succeeded.
func (g Gate) Resume() error { return g.set("open") }

func (g Gate) set(phase string) error {
	if !filepath.IsAbs(g.Root) {
		return errors.New("write gate root must be absolute")
	}
	raw, err := json.Marshal(gatePolicy{Schema: "filees.capacity-write-policy/v1", Phase: phase, ChangedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(g.Root, ".capacity-policy-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := preservePolicyOwner(f, filepath.Join(g.Root, "capacity-write-policy.json")); err != nil {
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
	if err := os.Rename(f.Name(), filepath.Join(g.Root, "capacity-write-policy.json")); err != nil {
		return err
	}
	return durable.SyncDirectory(g.Root)
}

// WithWriter holds a cross-process shared lease until all effects/children have
// finished, not merely until admission. Call it BEFORE service-WC/worker locks.
// A read from SVN itself needs no lease; a disk spool does.
func (g Gate) WithWriter(ctx context.Context, run func() error) error {
	if run == nil {
		return errors.New("write gate requires an operation")
	}
	if held, err := g.Held(); err != nil {
		return err
	} else if held {
		return ErrWriteHeld
	}
	return g.withLock(ctx, false, func() error {
		held, err := g.Held()
		if err != nil {
			return err
		}
		if held {
			return ErrWriteHeld
		}
		writer, err := g.beginWriter()
		if err != nil {
			return err
		}
		// Deliberately not deferred: panic/exit/SIGKILL must leave the durable
		// marker. The parent fd alone does not fence an orphan SVN child.
		result := run()
		return errors.Join(result, g.finishWriter(writer))
	})
}

// WithExclusive acknowledges completion of cooperative writers. Timeout or a
// failed callback preserves the previously installed stop, never resumes it.
func (g Gate) WithExclusive(ctx context.Context, run func() error) error {
	if run == nil {
		return errors.New("write gate requires an operation")
	}
	return g.withLock(ctx, true, func() error {
		if err := g.requireFinishedWriters(); err != nil {
			return err
		}
		return run()
	})
}
