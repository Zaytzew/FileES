//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type anchorShellRoot struct{ id, path string }

// Discovery is read-only and bounded. Native hiding independently checks the
// deterministic ID, registry/class bindings and missing path. No unregister.
func readAnchorShellRoots(sid string) ([]anchorShellRoot, error) {
	var roots []anchorShellRoot
	for _, hive := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		key, err := registry.OpenKey(hive, `Software\Microsoft\Windows\CurrentVersion\Explorer\SyncRootManager`, registry.READ)
		if err == registry.ErrNotExist {
			continue
		}
		if err != nil {
			return nil, err
		}
		names, err := key.ReadSubKeyNames(513)
		key.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(names) > 512 {
			return nil, errors.New("too many Shell roots to reconcile safely")
		}
		for _, name := range names {
			if !ownedShellID(name, sid) {
				continue
			}
			child, err := registry.OpenKey(hive, `Software\Microsoft\Windows\CurrentVersion\Explorer\SyncRootManager\`+name+`\UserSyncRoots`, registry.READ)
			if err != nil {
				continue
			}
			path, _, err := child.GetStringValue(sid)
			child.Close()
			if err != nil {
				continue
			}
			roots = append(roots, anchorShellRoot{id: name, path: path})
		}
	}
	return roots, nil
}

func ownedShellID(id, sid string) bool {
	prefix := "FileES!" + sid + "!"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	hash := strings.TrimPrefix(id, prefix)
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == 32 && hash == strings.ToLower(hash)
}

func shellPathProtected(path string, protected []string) bool {
	path = strings.ToLower(filepath.Clean(path))
	for _, keep := range protected {
		if !filepath.IsAbs(keep) {
			continue
		}
		keep = strings.ToLower(filepath.Clean(keep))
		if keep == path || strings.HasPrefix(keep, path+`\`) || strings.HasPrefix(path, keep+`\`) {
			return true
		}
	}
	return false
}

func obsoleteShellRoots(roots []anchorShellRoot, sid string, protected []string, missing func(string) bool) []anchorShellRoot {
	var result []anchorShellRoot
	seen := map[string]bool{}
	for _, root := range roots {
		path := filepath.Clean(root.path)
		if !ownedShellID(root.id, sid) || seen[root.id] || !filepath.IsAbs(path) ||
			strings.HasPrefix(path, `\\`) || filepath.Dir(path) == path || shellPathProtected(path, protected) || !missing(path) {
			continue
		}
		seen[root.id] = true
		result = append(result, root)
	}
	return result
}

// A best-effort sweep at startup and after lifecycle paths change, not another
// worker or a recurring registry scan. Busy relocation postpones it.
func (m *anchorManager) reconcileShell(ctx context.Context) bool {
	return m.reconcileShellWith(ctx, readAnchorShellRoots)
}

func (m *anchorManager) reconcileShellWith(ctx context.Context, discover func(string) ([]anchorShellRoot, error)) bool {
	if m.localIntentMu == nil || m.lifecycle == nil {
		return true
	}
	if !m.localIntentMu.TryLock() {
		return false
	}
	defer m.localIntentMu.Unlock()
	var protected []string
	for _, record := range m.lifecycle.List() {
		protected = append(protected, record.LocalPath, record.PendingLocalPath, record.PreservedAlternatePath)
	}
	sort.Strings(protected)
	hash := sha256.Sum256([]byte(strings.Join(protected, "\x00")))
	state := hex.EncodeToString(hash[:])
	if state == m.shellState {
		return true
	}
	m.shellState = state
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version, err := m.call(ctx, "", "version")
	if err != nil || !version.OK {
		return true
	}
	compatible := false
	for _, feature := range version.Features {
		compatible = compatible || feature == "shell_hide_obsolete_v1"
	}
	if !compatible {
		return true
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		m.log.Warnf("read Shell registration owner: %v", err)
		return true
	}
	sid := tokenUser.User.Sid.String()
	roots, err := discover(sid)
	if err != nil {
		m.log.Warnf("read Shell registrations: %v", err)
		return true
	}
	missing := func(path string) bool { _, err := os.Lstat(path); return errors.Is(err, os.ErrNotExist) }
	for _, root := range obsoleteShellRoots(roots, sid, protected, missing) {
		if ctx.Err() != nil {
			break
		}
		answer, err := m.call(ctx, "", "shell-hide-obsolete", "--root", root.path, "--registration-id", root.id)
		if err != nil || !answer.OK {
			m.log.Warnf("hide obsolete Shell entry: %s (%s): %v", answer.Error, answer.HResult, err)
		}
	}
	return true
}
