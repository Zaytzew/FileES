//go:build windows && !nocfapi

package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"filees/pkg/localrepo"
)

func TestObsoleteShellRootsProtectOwnerPathsAndUnknownState(t *testing.T) {
	const sid = "S-1-5-21-test"
	id := "FileES!" + sid + "!" + strings.Repeat("a", 64)
	roots := []anchorShellRoot{{id: id, path: `D:\old`}, {id: id, path: `D:\old`},
		{id: strings.Replace(id, sid, "different", 1), path: `D:\foreign`},
		{id: id + "x", path: `D:\malformed`},
		{id: strings.Replace(id, "aaaa", "bbbb", 1), path: `D:\active`},
		{id: strings.Replace(id, "aaaa", "cccc", 1), path: `D:\pending`},
		{id: strings.Replace(id, "aaaa", "dddd", 1), path: `D:\unknown`},
		{id: strings.Replace(id, "aaaa", "ffff", 1), path: `D:\`},
		{id: strings.Replace(id, "aaaa", "abab", 1), path: `D:\preserved\child`},
		{id: strings.Replace(id, "aaaa", "eeee", 1), path: `\\server\share\old`}}
	missing := func(path string) bool { return path != `D:\unknown` }
	got := obsoleteShellRoots(roots, sid, []string{`d:\ACTIVE`, `D:\pending\child`, `D:\preserved`}, missing)
	if len(got) != 1 || got[0].path != `D:\old` {
		t.Fatal(got)
	}
	if shellPathProtected(`D:\other`, []string{`D:\other-name`}) {
		t.Fatal("prefix confused")
	}
}

func TestShellSweepYieldsBeforeAccessingHelperWhenLifecycleBusy(t *testing.T) {
	store, err := localrepo.Open(t.TempDir() + "/lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	mu := new(sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	m := &anchorManager{localIntentMu: mu, lifecycle: store, helper: "must-not-start"}
	if m.reconcileShell(context.Background()) || m.shellState != "" {
		t.Fatal("busy sweep ran")
	}
}
