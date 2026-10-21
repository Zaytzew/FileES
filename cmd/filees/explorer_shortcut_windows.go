//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func (m *anchorManager) wireExplorerShortcut(ipc *ipcserver.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answer, err := m.call(ctx, "", "version")
	if err == nil && answer.OK && slices.Contains(answer.Features, "shell_visibility_v1") {
		ipc.SetExplorerShortcutService(m)
	}
}

// Serialize startup registration and changes; always reload the WC preference.
func (m *anchorManager) registerShellPreference(ctx context.Context, record localrepo.Record, icon string) error {
	leave, err := m.enterShellIntent(ctx)
	if err != nil {
		return err
	}
	defer leave()
	if m.lifecycle != nil {
		current, ok := m.lifecycle.Get(record.OperationID)
		if !ok || !sameShellTask(current, record) || current.State != localrepo.StateAttached ||
			(record.Anchor && (!current.Anchor || !m.virtualEnabled(current.ServerID))) {
			return errAnchorShellStale
		}
		record = current
	}
	// WinRT Register on an active native root is unsafe on this Windows.
	// Own leases drain first; never stop an untracked process to make it pass.
	if m.shellQueue.providerActive(record) {
		return commit.ErrLocalStatusBusy
	}
	info, err := m.call(ctx, "", "info", "--root", record.LocalPath)
	if err != nil {
		return err
	}
	if !info.OK || !info.Ours {
		return fmt.Errorf("cannot verify disconnected FileES root: %s %s", info.Error, info.HResult)
	}
	if !anchorProviderDisconnected(info.Status) {
		return commit.ErrLocalStatusBusy
	}
	args := []string{"shell-register", "--root", record.LocalPath, "--identity", record.ServerID + "\x1f" + record.RepoID, "--icon", icon}
	version, err := m.call(ctx, "", "version")
	if err != nil || !version.OK || !slices.Contains(version.Features, "shell_visibility_v1") {
		return errors.New("Explorer visibility requires an updated helper")
	}
	args = append(args, "--visible", strconv.FormatInt(int64(boolInt(record.ExplorerShortcut)), 10))
	answer, err := m.call(ctx, "", args...)
	if err != nil {
		return err
	}
	if !answer.OK {
		return fmt.Errorf("Explorer registration not confirmed: %s %s", answer.Error, answer.HResult)
	}
	return nil
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (m *anchorManager) ExplorerShortcut(ctx context.Context, serverID, repoID string, visible *bool, wcID string) (contract.RepoExplorerShortcutStatus, error) {
	state := contract.RepoExplorerShortcutStatus{ServerID: serverID, RepoID: repoID}
	if m.localIntentMu == nil || m.lifecycle == nil || !m.localIntentMu.TryLock() {
		return state, errors.New("another local folder operation is running")
	}
	defer m.localIntentMu.Unlock()
	if err := ctx.Err(); err != nil {
		return state, err
	}
	var record localrepo.Record
	for _, r := range m.lifecycle.List() {
		if r.ServerID == serverID && r.RepoID == repoID && r.Anchor && r.State == localrepo.StateAttached {
			record = r
			break
		}
	}
	if record.OperationID == "" || record.PendingLocalPath != "" || !m.virtualEnabled(serverID) {
		return state, errors.New("an attached virtual folder is required")
	}
	state.Visible = record.ExplorerShortcut
	state.WCID = record.OperationID
	if visible == nil {
		return state, nil
	}
	if wcID == "" || wcID != record.OperationID {
		return state, errors.New("the local folder changed; reopen its settings")
	}
	updated, err := m.lifecycle.SetExplorerShortcut(record.OperationID, *visible)
	if err != nil {
		return state, err
	}
	state.Visible = updated.ExplorerShortcut
	value := "0"
	if *visible {
		value = "1"
	}
	answer, err := m.call(ctx, "", "shell-visibility", "--root", record.LocalPath, "--visible", value)
	if err != nil || !answer.OK {
		m.queueShellRegistration(updated)
		return state, errors.New("saved Explorer visibility is queued for retry")
	}
	if m.guardChanged != nil {
		m.guardChanged(repoID)
	}
	return state, nil
}
