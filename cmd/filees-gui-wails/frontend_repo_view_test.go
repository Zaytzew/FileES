package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRepoViewIdleAndManualArchive(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ReplaceAll(embeddedFrontendFile(t, "frontend/repo-view.js"), "export function", "function")
	program := `const assert=require("node:assert/strict");
let stored=null;
const localStorage={getItem:()=>stored,setItem:(_,v)=>stored=v};
const window={dispatchEvent(){}};
` + source + `
const now=Date.parse("2026-09-09T12:00:00Z");
const repo=(days,id="r")=>({id,server_id:"s",can_fold_inactive:true,last_commit_at:new Date(now-days*86400000).toISOString()});
let prefs=readRepoView(); assert.equal(prefs.inactive,14); assert.equal(prefs.archive,30);
assert.equal(repoSection(repo(14),prefs,now),"active");
assert.equal(repoSection(repo(14.01),prefs,now),"inactive");
assert.equal(canArchive(repo(30),prefs,now),false);
assert.equal(canArchive(repo(31),prefs,now),true);
const old=repo(40); prefs.archived[repoViewKey(old)]=old.last_commit_at;
assert.equal(repoSection(old,prefs,now),"archived");
assert.equal(repoSection({...old,can_fold_inactive:false},prefs,now),"active");
assert.equal(repoSection(repo(1),prefs,now),"active");
assert.equal(repoSection({...old,last_commit_at:""},prefs,now),"active");
assert.equal(repoSection(repo(-1),prefs,now),"active");
assert.equal(repoSection(old,{...prefs,inactive:0,archive:0},now),"active");
saveRepoView(prefs); assert.deepEqual(readRepoView(),prefs);
const sorted=[repo(50,"old"),repo(2,"new"),repo(20,"mid")].sort(repoOrder);
assert.deepEqual(sorted.map(r=>r.id),["new","mid","old"]);
stored="null"; assert.equal(readRepoView().inactive,14);
stored='{"inactive":-1,"archive":"oops","archived":[]}'; assert.deepEqual(readRepoView(),{inactive:14,archive:30,archived:{}});
`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(program)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	html := embeddedFrontendFile(t, "frontend/index.html")
	for _, id := range []string{"inactive-days", "archive-days", "save-repo-view"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Fatal("missing control", id)
		}
	}
}
