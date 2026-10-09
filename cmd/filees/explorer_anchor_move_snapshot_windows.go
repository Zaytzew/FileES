//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"filees/pkg/client"
	"filees/pkg/localrepo"
	"filees/pkg/watcher"
)

func anchorMoveSnapshotDigest(p *watcher.TreeMoveSnapshot, record localrepo.Record, move client.RemoteTreeMove) (string, error) {
	if err := watcher.ValidateTreeMoveSnapshot(p, record.LocalPath); err != nil {
		return "", err
	}
	if p.Source != move.Source || p.Destination != move.Destination {
		return "", errors.New("tree move observations belong to another selection")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	if len(raw) > anchorMoveScannerLimit {
		return "", errors.New("tree move observations exceed size limit")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// Reads only the fixed name under the currently activated guarded root. A
// metadata-only admission precedes the ordinary-file read guard: no hydration.
// Digest is over EVERY persisted byte, not a size/mtime or a decoded approximation.
func readAnchorMoveSnapshot(ctx context.Context, record localrepo.Record, in anchorMoveIntent) (*watcher.TreeMoveSnapshot, error) {
	f, info, err := openAnchorReleaseFile(filepath.Join(anchorMoveStateDir(record), anchorMoveScannerName), false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.FileSizeHigh != 0 || info.FileSizeLow > anchorMoveScannerLimit {
		return nil, errors.New("tree move observations exceed size limit")
	}
	hash := sha256.New()
	limited := &io.LimitedReader{R: anchorReleaseReader{ctx, f}, N: anchorMoveScannerLimit + 1}
	d := json.NewDecoder(io.TeeReader(limited, hash))
	d.DisallowUnknownFields()
	p, err := decodeAnchorMoveSnapshot(d)
	if err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF || limited.N == 0 {
		return nil, errors.New("tree move observations have trailing data")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != in.ScannerSHA256 {
		return nil, errors.New("tree move observations differ from the immutable intent")
	}
	if err := watcher.ValidateTreeMoveSnapshot(p, record.LocalPath); err != nil {
		return nil, err
	}
	if p.Source != in.Move.Source || p.Destination != in.Move.Destination {
		return nil, errors.New("tree move observations belong to another selection")
	}
	return p, nil
}

// Bound the entry count WHILE parsing. A byte limit alone admits millions of
// tiny objects and lets json.Decode allocate a much larger slice before validation.
// Evidence written by us uses these exact unique keys, not case aliases.
func decodeAnchorMoveSnapshot(d *json.Decoder) (*watcher.TreeMoveSnapshot, error) {
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("tree move observations are not an object")
	}
	p := &watcher.TreeMoveSnapshot{}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return nil, errors.New("tree move observations have duplicate keys")
		}
		seen[key] = true
		var target any
		switch key {
		case "Schema":
			target = &p.Schema
		case "WC":
			target = &p.WC
		case "Source":
			target = &p.Source
		case "Destination":
			target = &p.Destination
		case "Session":
			target = &p.Session
		case "Sequence":
			target = &p.Sequence
		case "Entries":
			start, err := d.Token()
			if err != nil || start != json.Delim('[') {
				return nil, errors.New("tree move baseline is not an array")
			}
			for d.More() {
				if len(p.Entries) >= 100000 {
					return nil, errors.New("tree move has too many baseline entries")
				}
				var entry watcher.PublicationEntry
				if err := d.Decode(&entry); err != nil {
					return nil, err
				}
				p.Entries = append(p.Entries, entry)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			continue
		default:
			return nil, errors.New("tree move observations have unknown keys")
		}
		if err := d.Decode(target); err != nil {
			return nil, err
		}
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	return p, nil
}
