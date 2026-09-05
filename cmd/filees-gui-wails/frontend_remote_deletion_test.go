package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDeletedCopyDialogIsCompleteAndUsesGuardedDetach(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	html := embeddedFrontendFile(t, "frontend/index.html")
	css := embeddedFrontendFile(t, "frontend/app.css")
	for _, required := range []string{"data-copy-info", "aria-haspopup=\"dialog\"", "renderDeletedCopyDialog();"} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing info affordance %q", required)
		}
	}
	if !strings.Contains(html, "<dialog id=\"deleted-copy-dialog\"") || !strings.Contains(css, "overflow-wrap:anywhere") || !strings.Contains(css, "max-height:calc(100vh - 32px)") {
		t.Fatal("missing accessible responsive dialog")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable; presentation execution not tested")
	}
	start := strings.Index(script, "function deletedCopyRepo()")
	end := strings.Index(script, "async function triggerAction(")
	if start < 0 || end < start {
		t.Fatal("missing dialog functions")
	}
	program := `
const assert = require("node:assert/strict");
const nodes = new Map();
const $ = key => { if (!nodes.has(key)) nodes.set(key, {textContent:"", hidden:false, disabled:false, close(){this.closed=true;}}); return nodes.get(key); };
const repo = {id:"copy", server_id:"lab", server_deleted:true, local_copy_preserved:true, local_path:"/very/long/".repeat(100), display_name:"<b>user name</b>", local_copy_status:"changed", can_detach_local_copy:true};
let currentSnapshot = {repositories:[repo]};
let selectedDeletedCopy = {repoID:"copy", serverID:"lab"};
let deletedCopyReturnFocus = null;
const calls = [];
const GUIService = {Trigger:async request => {calls.push(request); return {accepted:true};}};
const actionErrors = {};
const showToast = () => {throw new Error("unexpected toast");};
` + script[start:end] + `
(async () => {
renderDeletedCopyDialog();
assert.equal($("#deleted-copy-name").textContent, repo.display_name);
assert.equal($("#deleted-copy-path").textContent, repo.local_path);
assert.match($("#deleted-copy-status").textContent, /dodatkowe pliki/);
assert.match($("#deleted-copy-cleanup").textContent, /usunięto .svn i .filees/);
assert.equal($("#detach-deleted-copy").disabled, false);
repo.local_cleanup_pending = true; repo.can_detach_local_copy = false; repo.cleanup_error = "<raw alpha stderr>\n".repeat(100);
renderDeletedCopyDialog();
assert.equal($("#deleted-copy-error").textContent, repo.cleanup_error);
assert.equal($("#deleted-copy-diagnostics").hidden, false);
assert.equal($("#detach-deleted-copy").disabled, true);
await detachDeletedCopy(); assert.equal(calls.length, 0);
repo.local_cleanup_pending = false; repo.can_detach_local_copy = true; repo.local_copy_status = "clean"; repo.cleanup_error = "";
renderDeletedCopyDialog();
assert.match($("#deleted-copy-status").textContent, /nie wykryto lokalnych zmian/);
assert.equal($("#deleted-copy-diagnostics").hidden, true);
repo.local_copy_status = "unknown"; renderDeletedCopyDialog();
assert.match($("#deleted-copy-status").textContent, /Nie udało się w pełni potwierdzić/);
await detachDeletedCopy();
assert.deepEqual(calls, [{kind:"detach_repository", repo_id:"copy", server_id:"lab"}]);
currentSnapshot.repositories = []; renderDeletedCopyDialog();
assert.equal($("#deleted-copy-dialog").closed, true);
})().catch(error => {console.error(error); process.exitCode=1;});
`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(program)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dialog execution: %v\n%s", err, out)
	}
}

func TestRemoteDeletionFrontendPreservesLocalWorkDistinction(t *testing.T) {
	script := embeddedFrontendFile(t, "frontend/app.js")
	models := embeddedFrontendFile(t, "frontend/bindings/filees/cmd/filees-gui-wails/models.js")
	for _, text := range []string{"repo.local_copy_preserved", "lokalne pliki zachowane", "zachowana kopia ze zmianami", "sprawdź zachowany folder", "sprzątanie metadanych czeka"} {
		if !strings.Contains(script, text) {
			t.Fatalf("missing terminal presentation %q", text)
		}
	}
	for _, field := range []string{"local_copy_preserved", "local_copy_status"} {
		if !strings.Contains(models, `this["`+field+`"] = undefined`) {
			t.Fatalf("missing binding %s", field)
		}
	}
}
