package storagewatch

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"filees/internal/durable"
)

const observationEpochName = ".observation-epoch"

func (g Gate) observationDirectory() string { return filepath.Join(g.Root, "state-observation") }

// ObservationStamp fences a live source read, not an authorization cache hit.
// Every admitted writer has a durable unique marker before its first effect;
// completion replaces the epoch BEFORE removing that marker. Thus even a whole
// mutation between two reads changes the stamp. Dirty/legacy stamps require
// source reconciliation; they never prove that an orphan child has finished.
func (g Gate) ObservationStamp() (stamp string, clean bool, err error) {
	if !filepath.IsAbs(g.Root) {
		return "", false, errors.New("observation root must be absolute")
	}
	dir := g.writerDirectory()
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "legacy", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privatePolicyMode(info.Mode()) {
		return "", false, ErrUnfinishedWriter
	}
	f, err := os.Open(dir)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	entries, err := f.ReadDir(4097)
	if err != nil && err != io.EOF {
		return "", false, err
	}
	if len(entries) > 4096 {
		return "", false, ErrUnfinishedWriter
	}
	// ReadDir on a file is not sorted. A stable stamp must be independent of
	// filesystem enumeration order and process restart.
	names := make([]string, 0, len(entries))
	clean = true
	for _, entry := range entries {
		names = append(names, entry.Name())
		clean = false
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		io.WriteString(h, name+"\x00")
	}
	observationDir := g.observationDirectory()
	if info, err := os.Lstat(observationDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privatePolicyMode(info.Mode()) {
			return "", false, ErrUnfinishedWriter
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	epoch, err := readObservationEpoch(filepath.Join(observationDir, observationEpochName))
	if errors.Is(err, os.ErrNotExist) {
		clean = false // old installation, no acknowledged generation yet
	} else if err != nil {
		return "", false, err
	}
	h.Write(epoch)
	return hex.EncodeToString(h.Sum(nil)), clean, nil
}

func readObservationEpoch(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !privatePolicyMode(info.Mode()) || info.Size() != 64 {
		return nil, ErrUnfinishedWriter
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrUnfinishedWriter
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil {
		return nil, err
	}
	if len(raw) != 64 {
		return nil, ErrUnfinishedWriter
	}
	if _, err := hex.DecodeString(string(raw)); err != nil {
		return nil, ErrUnfinishedWriter
	}
	return raw, nil
}

func (g Gate) advanceObservationEpoch() error {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	dir := g.observationDirectory()
	f, err := os.CreateTemp(dir, ".observation-pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := preservePolicyOwner(f, dir); err != nil {
		f.Close()
		return err
	}
	_, err = f.WriteString(hex.EncodeToString(token[:]))
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, observationEpochName)); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}
