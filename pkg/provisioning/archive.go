package provisioning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/privatefile"
	"github.com/google/uuid"
)

// ArchiveImport binds the external source, private carrier WC and final empty
// target to one create operation. Active means carrier publication acknowledged;
// Completed is the separate boundary that permits ordinary synchronization.
type ArchiveImport struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Size       int64  `json:"size"`
	Modified   string `json:"modified"`
	Digest     string `json:"digest,omitempty"`
	LoadedUUID string `json:"loaded_uuid,omitempty"`
	Completed  bool   `json:"completed,omitempty"`
}

const archiveCarrier = "filees-archive.svndump"

func (s *Store) CreateValidatedArchive(id, clientID, target, name, source string) (Operation, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Operation{}, err
	}
	if !filepath.IsAbs(source) || !filepath.IsAbs(target) {
		return Operation{}, errors.New("archive paths must be absolute")
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return Operation{}, err
	}
	if pathsOverlap(source, target) {
		return Operation{}, errors.New("archive must be outside the selected empty folder")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Operation{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return Operation{}, errors.New("archive must be a nonempty regular file")
	}
	if err := requireInitialContent(source, info); err != nil {
		return Operation{}, err
	}
	a := &ArchiveImport{Source: source, Target: filepath.Clean(target), Size: info.Size(), Modified: info.ModTime().UTC().Format(time.RFC3339Nano)}
	stage := filepath.Join(s.root, "archives", id, "carrier")
	return s.createValidated(id, clientID, stage, name, info.Size(), 1, a)
}

// PrepareArchive is bounded-memory and cancellation-aware. Only daemon-owned
// staging is written; source and target remain untouched. Restart after the
// snapshot boundary needs no access to the original file.
func (s *Store) PrepareArchive(ctx context.Context, id string) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	op, err := s.Get(id)
	if err != nil || op.Archive == nil {
		return op, err
	}
	want := filepath.Join(s.root, "archives", id, "carrier")
	if op.LocalPath != want {
		return Operation{}, errors.New("archive staging does not match its operation")
	}
	if op.Archive.Completed || op.Archive.LoadedUUID != "" {
		return op, nil
	}
	destination := filepath.Join(want, archiveCarrier)
	if op.Archive.Digest != "" {
		f, err := os.Open(destination)
		if err != nil {
			return Operation{}, err
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, archiveReader{ctx, f})
		if err != nil {
			return Operation{}, err
		}
		if n != op.Archive.Size || hex.EncodeToString(h.Sum(nil)) != op.Archive.Digest {
			return Operation{}, errors.New("archive snapshot changed")
		}
		return op, nil
	}
	f, err := os.Open(op.Archive.Source)
	if err != nil {
		return Operation{}, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return Operation{}, err
	}
	if !before.Mode().IsRegular() || before.Size() != op.Archive.Size || before.ModTime().UTC().Format(time.RFC3339Nano) != op.Archive.Modified {
		return Operation{}, errors.New("archive source changed after selection")
	}
	header := make([]byte, 64)
	n, err := io.ReadFull(f, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		return Operation{}, err
	}
	if !strings.HasPrefix(string(header[:n]), "SVN-fs-dump-format-version: ") {
		return Operation{}, errors.New("selected file is not an SVN archive")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Operation{}, err
	}
	if err := privatefile.EnsureDir(want); err != nil {
		return Operation{}, err
	}
	tmp, err := os.CreateTemp(want, ".snapshot-")
	if err != nil {
		return Operation{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := privatefile.Harden(tmp.Name()); err != nil {
		return Operation{}, err
	}
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(archiveReader{ctx, f}, op.Archive.Size+1))
	if err != nil {
		return Operation{}, err
	}
	after, err := f.Stat()
	if err != nil {
		return Operation{}, err
	}
	current, err := os.Stat(op.Archive.Source)
	if err != nil {
		return Operation{}, err
	}
	if written != op.Archive.Size || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, current) || !current.ModTime().Equal(before.ModTime()) {
		return Operation{}, errors.New("archive source changed while taking snapshot")
	}
	if err := tmp.Sync(); err != nil {
		return Operation{}, err
	}
	if err := tmp.Close(); err != nil {
		return Operation{}, err
	}
	if err := os.Rename(tmp.Name(), destination); err != nil {
		return Operation{}, err
	}
	return s.mutate(id, func(op *Operation) error { op.Archive.Digest = hex.EncodeToString(h.Sum(nil)); return nil })
}

func (s *Store) MarkArchiveLoaded(id, loadedUUID string) (Operation, error) {
	return s.mutate(id, func(op *Operation) error {
		if op.Archive == nil || op.State != StateActive || op.Archive.Digest == "" {
			return errors.New("archive carrier is not published")
		}
		if _, err := uuid.Parse(loadedUUID); err != nil {
			return err
		}
		if op.Archive.LoadedUUID != "" && op.Archive.LoadedUUID != loadedUUID {
			return errors.New("archive generation changed")
		}
		op.Archive.LoadedUUID = loadedUUID
		return nil
	})
}

func (s *Store) CompleteArchive(id string) (Operation, error) {
	return s.mutate(id, func(op *Operation) error {
		if op.Archive == nil || op.State != StateActive || op.Archive.LoadedUUID == "" {
			return fmt.Errorf("archive generation is not confirmed")
		}
		op.Archive.Completed = true
		return nil
	})
}

// DiscardCompletedArchive removes only this operation's private carrier WC,
// after the final target is durable. Refuse redirected directories rather than
// deleting an archive, target or unrelated user's files through an alias.
func (s *Store) DiscardCompletedArchive(id string) error {
	op, err := s.Get(id)
	if err != nil {
		return err
	}
	if op.Archive == nil || !op.Archive.Completed {
		return errors.New("archive import is not complete")
	}
	dir := filepath.Join(s.root, "archives", id)
	if op.LocalPath != filepath.Join(dir, "carrier") || pathsOverlap(dir, op.Archive.Source) || pathsOverlap(dir, op.Archive.Target) {
		return errors.New("archive cleanup path does not match private staging")
	}
	for _, path := range []string{s.root, filepath.Join(s.root, "archives"), dir} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive staging directory was redirected")
		}
	}
	return os.RemoveAll(dir)
}

type archiveReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r archiveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (op Operation) ArchivePending() bool { return op.Archive != nil && !op.Archive.Completed }

func sameArchiveInput(a, b *ArchiveImport) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Source == b.Source && a.Target == b.Target && a.Size == b.Size && a.Modified == b.Modified
}
