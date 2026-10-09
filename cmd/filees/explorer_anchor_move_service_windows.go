//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/watcher"
	"github.com/google/uuid"
)

type anchorTreeMoveAdapter struct{ manager *anchorManager }

var errTreeMovePreview = errors.New("tree move preview, no effect or scanner ACK")

func (m *anchorManager) wireTreeMove(ipc *ipcserver.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := m.call(ctx, "", "version")
	if err != nil || !version.OK {
		return
	}
	for _, feature := range version.Features {
		if feature == "bound_tree_move_v1" {
			ipc.SetAnchorMove(anchorTreeMoveAdapter{m})
			return
		}
	}
}
func (m *anchorManager) moveRecord(server, repoID string) (localrepo.Record, error) {
	if m.lifecycle != nil && m.virtualEnabled(server) {
		for _, record := range m.lifecycle.List() {
			if record.ServerID == server && record.RepoID == repoID && record.Anchor && record.State == localrepo.StateAttached {
				if err := m.currentReleaseRecord(record); err != nil {
					return localrepo.Record{}, err
				}
				if r := m.repos(server, repoID); r != nil && r.Summary().Access == "rw" {
					return record, nil
				}
			}
		}
	}
	return localrepo.Record{}, ipcserver.ErrDepthUnavailable
}
func (m *anchorManager) selectedMove(ctx context.Context, p contract.RepoAnchorMoveSelection) (localrepo.Record, client.RemoteTreeMove, error) {
	record, err := m.moveRecord(p.ServerID, p.RepoID)
	if err != nil {
		return record, client.RemoteTreeMove{}, err
	}
	guards, root, err := guardAnchorMoveState(record)
	if err != nil {
		return record, client.RemoteTreeMove{}, err
	}
	defer closeAnchorReleaseGuards(guards)
	tree, err := loadAnchorRemoteTree(record, root)
	if err != nil {
		return record, client.RemoteTreeMove{}, err
	}
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return record, client.RemoteTreeMove{}, err
	}
	reader, ok := svn.(interface {
		HistoryRepositoryUUID(context.Context, string) (string, error)
	})
	if !ok {
		return record, client.RemoteTreeMove{}, errors.ErrUnsupported
	}
	id, err := reader.HistoryRepositoryUUID(ctx, record.RepoURL)
	if err != nil {
		return record, client.RemoteTreeMove{}, err
	}
	move := client.RemoteTreeMove{RootURL: record.RepoURL, UUID: id, BaseRevision: tree.Revision, OperationID: uuid.NewString(), Source: p.Source, Destination: p.Destination, Message: "FileES: move folder"}
	return record, move, client.ValidateRemoteTreeMove(move)
}

// Consent excludes fresh operation ID/session sequence only. Attachment, OS
// FileIDs, complete remote generation and published baseline remain bound.
func treeMoveConsent(proof *anchorMoveObjects, snapshot *watcher.TreeMoveSnapshot) (string, error) {
	p := *proof
	p.Move.OperationID = "11111111-1111-4111-8111-111111111111"
	s := *snapshot
	s.Session = ""
	s.Sequence = 0
	raw, err := json.Marshal(struct {
		Objects  anchorMoveObjects
		Baseline watcher.TreeMoveSnapshot
	}{p, s})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}
func (a anchorTreeMoveAdapter) MovePlan(ctx context.Context, p contract.RepoAnchorMoveSelection) (contract.RepoAnchorMovePlan, error) {
	var out contract.RepoAnchorMovePlan
	record, move, err := a.manager.selectedMove(ctx, p)
	if err != nil {
		return out, err
	}
	err = a.manager.moveTree(ctx, record, move, func(proof *anchorMoveObjects, snapshot *watcher.TreeMoveSnapshot) error {
		digest, err := treeMoveConsent(proof, snapshot)
		if err != nil {
			return err
		}
		out = contract.RepoAnchorMovePlan{Fingerprint: digest, Source: move.Source, Destination: move.Destination}
		return errTreeMovePreview
	})
	if errors.Is(err, errTreeMovePreview) {
		return out, nil
	}
	return out, err
}
func (a anchorTreeMoveAdapter) MoveExecute(ctx context.Context, p contract.RepoAnchorMovePayload) error {
	record, move, err := a.manager.selectedMove(ctx, p.RepoAnchorMoveSelection)
	if err != nil {
		return err
	}
	return a.manager.moveTree(ctx, record, move, func(proof *anchorMoveObjects, snapshot *watcher.TreeMoveSnapshot) error {
		digest, err := treeMoveConsent(proof, snapshot)
		if err != nil {
			return err
		}
		if digest != p.Fingerprint {
			return ipcserver.ErrAnchorSelectionChanged
		}
		return nil
	})
}
func (a anchorTreeMoveAdapter) MoveRecover(ctx context.Context, server, repoID string) error {
	record, err := a.manager.moveRecord(server, repoID)
	if err != nil {
		return err
	}
	if !commit.HasPendingRemoteTree(record.LocalPath) {
		return nil
	}
	a.manager.mu.Lock()
	connection := a.manager.running[anchorKey(record)]
	a.manager.mu.Unlock()
	if connection.bridge == nil {
		return ipcserver.ErrDepthUnavailable
	}
	return a.manager.recoverTreeMove(ctx, record, connection.bridge)
}
func (m *anchorManager) recoverConnectedTreeMove(ctx context.Context, record localrepo.Record, bridge *anchorBridge) {
	if !commit.HasPendingRemoteTree(record.LocalPath) {
		return
	}
	// The provider may connect before the ordinary RW starter binds its
	// writer/scanner. Wait only for that local readiness, not uncertain remote
	// outcomes, damaged evidence or publication. No new remote request is sent.
	waitUntil := time.Now().Add(time.Minute)
	for ctx.Err() == nil {
		err := m.recoverTreeMove(ctx, record, bridge)
		if err == nil || ctx.Err() != nil {
			return
		}
		if (!errors.Is(err, ipcserver.ErrDepthUnavailable) && !errors.Is(err, commit.ErrLocalStatusBusy)) || !time.Now().Before(waitUntil) {
			m.log.Warnf("tree move recovery retained its fence: %v", err)
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
