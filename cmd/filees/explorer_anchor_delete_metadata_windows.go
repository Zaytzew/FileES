//go:build windows && !nocfapi

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
)

// Explicit byte counts are required deletion evidence. The older file-state
// response omits these fields; it must not silently turn that absence into 0.
type anchorDeleteFileState struct {
	cfapiAnswer
	sizesKnown bool
}

func decodeAnchorDeleteFileState(raw []byte) (anchorDeleteFileState, error) {
	var result anchorDeleteFileState
	var document struct {
		Schema string `json:"schema"`
		OK     bool   `json:"ok"`
		States []struct {
			cfapiAnswer
			OnDisk   *int64 `json:"on_disk"`
			Modified *int64 `json:"modified"`
		} `json:"states"`
	}
	if len(raw) > 16*1024 {
		return result, errors.New("file deletion metadata exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&document); err != nil {
		return result, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return result, errors.New("file deletion metadata has trailing data")
	}
	if document.Schema != "filees.cfapi/v1" || !document.OK || len(document.States) != 1 {
		return result, errors.New("file deletion metadata is incomplete")
	}
	row := document.States[0]
	if !row.OK || row.OnDisk == nil || row.Modified == nil || *row.OnDisk < 0 || *row.Modified < 0 {
		return result, errors.New("file deletion byte counts are unavailable")
	}
	result.cfapiAnswer = row.cfapiAnswer
	result.OnDisk, result.Modified = *row.OnDisk, *row.Modified
	result.sizesKnown = true
	return result, nil
}

func inspectAnchorDeleteFileState(ctx context.Context, helper, root, rel string) (anchorDeleteFileState, error) {
	if _, ok := cleanAnchorIdentity(rel); !ok || strings.ContainsAny(rel, "\x00\r\n\t") {
		return anchorDeleteFileState{}, errors.New("invalid deletion metadata path")
	}
	command := exec.CommandContext(ctx, helper, "file-states", "--root", root)
	command.Stdin = strings.NewReader(rel + "\n")
	output, err := command.StdoutPipe()
	if err != nil {
		return anchorDeleteFileState{}, err
	}
	if err = command.Start(); err != nil {
		return anchorDeleteFileState{}, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(output, 16*1024+1))
	if readErr != nil || len(raw) > 16*1024 {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if readErr != nil || waitErr != nil {
		return anchorDeleteFileState{}, errors.Join(readErr, waitErr)
	}
	return decodeAnchorDeleteFileState(raw)
}
