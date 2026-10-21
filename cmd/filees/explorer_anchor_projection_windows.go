//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	"filees/pkg/localrepo"
)

func wireAnchorProjection(p *daemonProvisioner, m *anchorManager) {
	p.projectAnchorWorkItem = m.projectSVNWorkItem
	p.completeAnchorWorkItems = m.completeSVNWorkItems
}

func anchorReprojectionPath(root string) string {
	return filepath.Join(root, ".filees", "anchor-reprojection.json")
}

// This is not reachable through a public IPC verb or Explorer's delete
// coordinator. The native child invokes it under the existing WC writer.
// Never wait for that writer again, call SVN, or grant a new remote delete.
func (m *anchorManager) projectSVNWorkItem(ctx context.Context, repo config.Repo, request client.ProjectionRemoval) error {
	if !repo.Anchor || request.Root != filepath.ToSlash(repo.LocalPath) && !strings.EqualFold(filepath.Clean(request.Root), filepath.Clean(repo.LocalPath)) || m.lifecycle == nil {
		return errors.New("SVN projection attachment differs")
	}
	if !m.virtualEnabled(repo.ServerID) {
		return errors.New("SVN projection virtual folders are disabled")
	}
	// Opposite lock ordering is intentional only with NONBLOCKING admission:
	// an explicit action may hold intent while waiting for this WC writer.
	leave, err := m.enterObserverIntent()
	if err != nil {
		return err
	}
	defer leave()
	record, err := m.svnProjectionRecord(repo)
	if err != nil {
		return err
	}
	return m.projectSVNWorkItemHeld(ctx, record, request)
}

func (m *anchorManager) svnProjectionRecord(repo config.Repo) (localrepo.Record, error) {
	var record localrepo.Record
	if !repo.Anchor || m.lifecycle == nil || !m.virtualEnabled(repo.ServerID) {
		return record, errors.New("SVN projection attachment is not enabled")
	}
	for _, current := range m.lifecycle.List() {
		if current.State == localrepo.StateAttached && current.Anchor && current.ServerID == repo.ServerID && current.RepoID == repo.ID && current.LocalPath == repo.LocalPath && current.RepoURL == repo.RepoURL {
			if record.OperationID != "" {
				return record, errors.New("ambiguous projection attachment")
			}
			record = current
		}
	}
	if record.OperationID == "" || m.currentReleaseRecord(record) != nil || commit.HasPendingLocalTransaction(record.LocalPath) {
		return record, errors.New("SVN projection attachment is not available")
	}
	return record, nil
}

func (m *anchorManager) projectSVNWorkItemHeld(ctx context.Context, record localrepo.Record, request client.ProjectionRemoval) error {
	m.mu.Lock()
	connection := m.running[anchorKey(record)]
	m.mu.Unlock()
	if connection.ctx == nil || connection.ctx.Err() != nil || connection.providerCtx == nil || connection.providerCtx.Err() != nil || connection.bridge == nil || connection.repoURL != record.RepoURL || connection.root != record.LocalPath {
		return fmt.Errorf("%w: SVN projection provider is not connected", commit.ErrLocalStatusBusy)
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(connection.providerCtx, cancel)
	defer func() { stop(); cancel() }()
	guards, root, err := guardAnchorMoveState(record)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	parents, err := guardAnchorReleaseDirectories(filepath.Dir(filepath.Join(record.LocalPath, filepath.FromSlash(request.Path))), record.LocalPath)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(parents)
	tree, err := loadAnchorRemoteTree(record, root)
	if err != nil {
		return err
	}
	state, err := inspectAnchorDeleteFileState(ctx, m.helper, record.LocalPath, request.Path)
	if err != nil {
		return err
	}
	rel, revision, size, valid := parseAnchorRemoteIdentity(state.Identity)
	if !valid || rel != request.Path || revision > tree.Revision || state.FileID != request.FileID || !anchorDeleteRemoteState(state, state.Identity) {
		return fmt.Errorf("SVN projection refused: identity_valid=%t path_matches=%t revision=%d bound_revision=%d file_id_matches=%t placeholder=%t local=%t in_sync=%t on_disk=%d modified=%d metrics_known=%t",
			valid, rel == request.Path, revision, tree.Revision, state.FileID == request.FileID, state.Placeholder, state.Local, state.InSync, state.OnDisk, state.Modified, state.sizesKnown)
	}
	var selected *anchorRemoteNode
	for i := range tree.Nodes {
		node := &tree.Nodes[i]
		if node.Path == request.Path && node.Kind == "file" && node.Size == size {
			selected = node
		}
	}
	if selected == nil {
		return errors.New("SVN projection path is absent from the bound manifest")
	}
	// A durable hint is NOT deletion authority. It only forces the existing
	// remote-tree observer to recreate this entry after successful SVN replay,
	// even if the server revision is unchanged. Keep the canonical tree intact.
	hints, err := loadAnchorRemoteTreeAt(record, root, anchorReprojectionPath(record.LocalPath))
	if os.IsNotExist(err) {
		hints = tree
		hints.Nodes = nil
		err = nil
	}
	if err != nil {
		return err
	}
	if tree.Revision > hints.Revision {
		hints.Revision = tree.Revision
	}
	seen := make(map[string]bool, len(hints.Nodes))
	for _, node := range hints.Nodes {
		seen[node.Path] = true
	}
	for _, node := range tree.Nodes {
		if !seen[node.Path] && (node.Path == request.Path || node.Kind == "dir" && strings.HasPrefix(request.Path, node.Path+"/")) {
			hints.Nodes = append(hints.Nodes, node)
		}
	}
	if err := validateAnchorRemoteNodes(hints.Nodes); err != nil {
		return err
	}
	hints.Projection = request.Operation + ":" + strconv.FormatUint(uint64(request.Sequence), 10)
	if err := saveAnchorRemoteTreeAt(record, hints, anchorReprojectionPath(record.LocalPath)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return connection.bridge.connectedCommand(ctx, "recovery-remove", "tree-set", request.Path, state.Identity,
		strconv.FormatInt(size, 10), request.FileID, state.USN)
}

// The service still owns its writer and pending fence. Restore the pinned
// virtual view before allowing watcher checkpoints; do not ask SVN or wait
// for this same writer here. Hints remain for the subsequent HEAD refresh.
func (m *anchorManager) completeSVNWorkItems(ctx context.Context, repo config.Repo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(anchorReprojectionPath(repo.LocalPath)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	leave, err := m.enterObserverIntent()
	if err != nil {
		return err
	}
	defer leave()
	record, err := m.svnProjectionRecord(repo)
	if err != nil {
		return err
	}
	guards, root, err := guardAnchorMoveState(record)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	hints, err := loadAnchorRemoteTreeAt(record, root, anchorReprojectionPath(record.LocalPath))
	if err != nil {
		return err
	}
	if len(hints.Nodes) == 0 {
		return nil
	}
	tree, err := loadAnchorRemoteTree(record, root)
	if err != nil {
		return err
	}
	m.mu.Lock()
	connection := m.running[anchorKey(record)]
	m.mu.Unlock()
	if connection.ctx == nil || connection.ctx.Err() != nil || connection.providerCtx == nil || connection.providerCtx.Err() != nil || connection.bridge == nil || connection.root != record.LocalPath || connection.repoURL != record.RepoURL {
		return fmt.Errorf("%w: SVN reprojection provider is not connected", commit.ErrLocalStatusBusy)
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(connection.providerCtx, cancel)
	defer func() { stop(); cancel() }()
	wanted := make(map[string]bool, len(hints.Nodes))
	for _, node := range hints.Nodes {
		wanted[node.Path] = true
	}
	for _, change := range anchorTreeChanges(nil, tree.Nodes) {
		node := change.node
		if !wanted[node.Path] {
			continue
		}
		parents, err := guardAnchorReleaseDirectories(filepath.Dir(filepath.Join(record.LocalPath, filepath.FromSlash(node.Path))), record.LocalPath)
		if err != nil {
			return err
		}
		kind, size := "f", node.Size
		if node.Kind == "dir" {
			kind, size = "d", 0
		}
		err = connection.bridge.connectedCommand(ctx, "tree-set", "tree-set", node.Path, kind, strconv.FormatInt(size, 10), "", node.Identity, "0")
		closeAnchorReleaseGuards(parents)
		// An ordinary/dirty directory is a retained container, never a
		// permission to replace local data or erase its children.
		if err != nil && !(node.Kind == "dir" && strings.HasSuffix(err.Error(), "0x00000001")) {
			return err
		}
	}
	return ctx.Err()
}
