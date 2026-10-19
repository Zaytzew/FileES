//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"time"

	"filees/pkg/client"
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

type anchorDeleteResidentWait struct{ Request client.RemoteFileDelete }

func (anchorDeleteResidentWait) Error() string {
	return "verifying already downloaded file bytes; wait briefly and repeat the delete"
}

// A single bounded slot per connected bridge, not a path cache or permission.
// A new OS request, current authorization, metadata/manifest proof and matching
// USN/FileID are still mandatory. No automatic OS deletion follows a refusal.
type anchorDeleteResidentProof struct {
	Callback  anchorDeleteCallback
	USN, SHA1 string
}

func (p *anchorDeleteResidentProof) matches(cb anchorDeleteCallback) bool {
	return p.Callback.Volume == cb.Volume && p.Callback.RootFileID == cb.RootFileID && p.Callback.FileID == cb.FileID && p.Callback.Identity == cb.Identity && p.Callback.NormalizedPath == cb.NormalizedPath
}

func (m *anchorManager) proveResidentDelete(record localrepo.Record, cb anchorDeleteCallback, request client.RemoteFileDelete) (*anchorDeleteResidentProof, error) {
	m.mu.Lock()
	connection := m.running[anchorKey(record)]
	m.mu.Unlock()
	if connection.providerCtx == nil {
		return nil, errors.New("file deletion provider is no longer connected")
	}
	ctx, cancel := context.WithTimeout(connection.providerCtx, 30*time.Second)
	defer cancel()
	// Leave the callback/OS delete handle first: reading data while a delete
	// notification awaits ACK can block in cldflt even for resident ranges.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	leave, err := m.enterMoveIntent(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	repo, _, _, err := m.deleteAccess(record)
	if err != nil {
		return nil, err
	}
	var proof *anchorDeleteResidentProof
	err = repo.WithLocalMetadataGuard(ctx, func() error {
		if err := m.currentReleaseRecord(record); err != nil {
			return err
		}
		guards, root, err := guardAnchorMoveState(record)
		if err != nil {
			return err
		}
		defer closeAnchorReleaseGuards(guards)
		if root.Volume != cb.Volume || root.Index != cb.RootFileID {
			return errors.New("file deletion root changed before content verification")
		}
		svn, err := m.svn(record.ServerID)
		if err != nil {
			return err
		}
		reader, ok := svn.(client.LocalFileDeletePreflighter)
		if !ok {
			return errors.ErrUnsupported
		}
		metadata, err := reader.LocalFileDeletePreflight(ctx, record.LocalPath, request)
		if err != nil {
			return err
		}
		before, err := inspectAnchorDeleteFileState(ctx, m.helper, record.LocalPath, request.Source)
		if err != nil {
			return err
		}
		if !anchorDeleteResidentState(before, cb.Identity, metadata.ChecksumSHA1) {
			return errors.New("file changed or is not fully resident; no content read performed")
		}
		pin, info, err := pinMoveObject(record, request.Source, false)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(pin)
		if anchorReleaseID(info) != (anchorReleaseFileID{cb.Volume, cb.FileID}) {
			return errors.New("file replaced before content verification")
		}
		if err := verifyAnchorDeleteContents(ctx, pin, *before.Size, metadata.ChecksumSHA1); err != nil {
			return err
		}
		after, err := inspectAnchorDeleteFileState(ctx, m.helper, record.LocalPath, request.Source)
		if err != nil {
			return err
		}
		if !anchorDeleteResidentState(after, cb.Identity, metadata.ChecksumSHA1) || after.USN != before.USN {
			return errors.New("file changed during content verification")
		}
		proof = &anchorDeleteResidentProof{Callback: cb, USN: after.USN, SHA1: metadata.ChecksumSHA1}
		return ctx.Err()
	})
	return proof, err
}
