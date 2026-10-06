//go:build windows && !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"filees/pkg/commit"
	"golang.org/x/sys/windows"
)

const anchorReleaseFenceSchema = "filees.explorer-release-pending/v1"

type anchorReleaseFenceRecord struct {
	Schema string
	Record anchorReleaseRecord // Exact initial retained evidence, never a receipt.
}

type anchorReleaseFence struct {
	record anchorReleaseFenceRecord
	file   anchorReleaseFileID
}

func anchorReleaseFencePath(wc string) string {
	return filepath.Join(wc, ".filees", "state", commit.FileReleasePendingName)
}

func readAnchorReleaseFenceFile(ctx context.Context, f *os.File) (anchorReleaseFenceRecord, error) {
	var out anchorReleaseFenceRecord
	d := json.NewDecoder(anchorReleaseReader{ctx, io.LimitReader(f, 16*1024+1)})
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return out, errors.New("release fence has trailing data")
	}
	if out.Schema != anchorReleaseFenceSchema || out.Record.Phase != "retained" || out.Record.Version != 2 {
		return out, errors.New("unsupported release fence; preserve retained data")
	}
	return out, ctx.Err()
}

// expected's location comes from the currently activated lifecycle record;
// store comes from the daemon's own data root, never a path read from this file.
// The saved relative path/revision remain subject to journal and native checks.
func loadAnchorReleaseFence(ctx context.Context, store string, expected anchorReleaseIdentity) (*anchorReleaseJournal, *anchorReleaseFence, error) {
	if !filepath.IsAbs(expected.WC) || filepath.Clean(expected.WC) != expected.WC || expected.ServerID == "" || expected.RepoID == "" || expected.RepoURL == "" {
		return nil, nil, errors.New("release recovery requires an activated local identity")
	}
	guards, err := guardAnchorReleaseDirectories(filepath.Dir(anchorReleaseFencePath(expected.WC)), expected.WC)
	if err != nil {
		return nil, nil, err
	}
	defer closeAnchorReleaseGuards(guards)
	f, info, err := openAnchorReleaseFile(anchorReleaseFencePath(expected.WC), false)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	if info.FileSizeHigh != 0 || info.FileSizeLow > 16*1024 {
		return nil, nil, errors.New("release fence exceeds size limit")
	}
	raw, err := readAnchorReleaseFenceFile(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	i := raw.Record.Identity
	if i.WC != expected.WC || i.ServerID != expected.ServerID || i.RepoID != expected.RepoID || i.RepoURL != expected.RepoURL {
		return nil, nil, errors.New("release fence differs from activated repository")
	}
	j := &anchorReleaseJournal{store: store, identity: i, id: raw.Record.ID}
	r, err := j.inspectRecords(ctx)
	if err != nil {
		return nil, nil, err
	}
	r.Phase = "retained"
	if r != raw.Record {
		return nil, nil, errors.New("release fence differs from retained evidence")
	}
	return j, &anchorReleaseFence{record: raw, file: anchorReleaseID(info)}, nil
}

func (j *anchorReleaseJournal) persistFence(ctx context.Context) (*anchorReleaseFence, error) {
	r, err := j.inspect(ctx)
	if err != nil {
		return nil, err
	}
	if r.Phase != "retained" {
		return nil, errors.New("release fence must precede every effect")
	}
	dir := filepath.Dir(anchorReleaseFencePath(j.identity.WC))
	guards, err := guardAnchorReleaseDirectories(dir, j.identity.WC)
	if err != nil {
		return nil, err
	}
	defer closeAnchorReleaseGuards(guards)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeAnchorReleaseJSONFile(dir, commit.FileReleasePendingName, anchorReleaseFenceRecord{Schema: anchorReleaseFenceSchema, Record: r}); err != nil {
		return nil, err
	}
	_, fence, err := loadAnchorReleaseFence(ctx, j.store, j.identity)
	return fence, err
}

// Called only after actual restored/idle/retirement proof AND durable scanner
// acknowledgement, still under the WC writer guard. Never a TTL or cleanup.
// Metadata directories and the exact no-write/no-replace marker handle stay
// guarded until deletion; an equivalent replacement at the pathname refuses.
func (j *anchorReleaseJournal) clearFence(ctx context.Context, fence *anchorReleaseFence) error {
	if fence == nil || fence.record.Record.ID != j.id || fence.record.Record.Identity != j.identity {
		return errors.New("release fence identity differs")
	}
	guards, err := guardAnchorReleaseDirectories(filepath.Dir(anchorReleaseFencePath(j.identity.WC)), j.identity.WC)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	r, err := j.inspectRecords(ctx)
	if err != nil {
		return err
	}
	if r.Phase != "restore_requested" {
		return errors.New("release fence cannot clear an unfinished mutation")
	}
	done, err := readAnchorReleaseRetirement(ctx, filepath.Join(j.store, j.id, "05.json"))
	if err != nil || done.Version != 1 || done.Record != r || done.Copy.Index == 0 {
		return errors.Join(err, errors.New("release retirement is not confirmed"))
	}
	intent, err := readAnchorReleaseRetirement(ctx, filepath.Join(j.store, j.id, "04.json"))
	if err != nil || intent != done {
		return errors.Join(err, errors.New("release retirement intent differs"))
	}
	if _, err := os.Lstat(filepath.Join(j.store, j.id, "data.bin")); !os.IsNotExist(err) {
		return errors.New("retained bytes remain; do not clear recovery fence")
	}
	wide, err := windows.UTF16PtrFromString(anchorReleaseFencePath(j.identity.WC))
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(wide, windows.GENERIC_READ|windows.DELETE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), anchorReleaseFencePath(j.identity.WC))
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	forbidden := uint32(windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT | windows.FILE_ATTRIBUTE_OFFLINE | 0x00040000 | 0x00400000)
	if info.FileAttributes&forbidden != 0 || info.NumberOfLinks != 1 || anchorReleaseID(info) != fence.file || info.FileSizeHigh != 0 || info.FileSizeLow > 16*1024 {
		return errors.New("release fence was replaced or is not an ordinary local file")
	}
	got, err := readAnchorReleaseFenceFile(ctx, f)
	if err != nil || got != fence.record {
		return errors.Join(err, errors.New("release fence content changed"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	remove := byte(1)
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &remove, 1)
}
