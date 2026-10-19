//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// refresh observes the entire namespace again while KEEPING the original
// deny-write data and stream handles open. Only their primary-data digests are
// reused; identities, paths, stream bytes and metadata are observed afresh.
// No timestamp, persistent cache, or directory stamp is a content proof.
// After an executor release/restart or the rename custody gap, pinOld instead
// acquires and hashes a fresh tree. Retirement still hashes each source file
// after opening its DELETE handle, closing the final custody-transfer gap.
func (p *conversionTreePin) refresh(ctx context.Context, root string) ([]conversionObject, error) {
	if p == nil || p.Root != filepath.Clean(root) || len(p.Files) != len(p.Objects) || len(p.Objects) == 0 {
		return nil, errors.New("conversion source custody is incomplete")
	}
	known := make(map[string]bool, len(p.Objects))
	for _, node := range p.Objects {
		known[node.Path] = true
	}
	seen := map[string]bool{".": true}
	objects := make([]conversionObject, 0, len(p.Objects))
	for _, prior := range p.Objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file := p.Files[prior.Path]
		if file == nil {
			return nil, errors.New("conversion source handle disappeared")
		}
		var held windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &held); err != nil {
			return nil, fmt.Errorf("conversion source custody lost: %w", err)
		}
		if conversionID(held) != prior.ID || held.NumberOfLinks != 1 {
			return nil, errors.New("conversion source handle identity changed")
		}
		path := filepath.Join(root, filepath.FromSlash(prior.Path))
		meta, info, err := conversionOpen(path, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
		if err != nil {
			return nil, err
		}
		meta.Close()
		if conversionID(info) != prior.ID || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != prior.Directory {
			return nil, errors.New("conversion source path no longer names the held object")
		}
		got := prior
		got.Size = int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow))
		got.Attributes, got.Created, got.Modified = info.FileAttributes, info.CreationTime, info.LastWriteTime
		got.DACL, err = conversionDACL(file)
		if err != nil {
			return nil, err
		}
		// ADS are small, independently mutable metadata. Never reuse their hash
		// merely because the main stream's data handle is still held.
		if err = conversionCheckStreams(ctx, file, prior.Streams); err != nil {
			return nil, err
		}
		for _, stream := range p.Streams[prior.Path] {
			if err = windows.GetFileInformationByHandle(windows.Handle(stream.Fd()), &held); err != nil {
				return nil, fmt.Errorf("conversion source stream custody lost: %w", err)
			}
		}
		objects = append(objects, got)
		if !prior.Directory {
			continue
		}
		entries, err := readPinnedMoveDirectory(windows.Handle(file.Fd()), path)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if name == "" || strings.ContainsAny(name, "\\/:\x00") {
				return nil, errors.New("unsafe conversion source entry")
			}
			child := name
			if prior.Path != "." {
				child = prior.Path + "/" + name
			}
			if !known[child] || seen[child] {
				return nil, errors.New("conversion source namespace changed during custody")
			}
			seen[child] = true
		}
	}
	if len(seen) != len(known) {
		return nil, errors.New("conversion source entry disappeared during custody")
	}
	return objects, nil
}
