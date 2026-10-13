package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestStoreVersionCardShowsLocalReleaseAndOnlyStoreActions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source := embeddedFrontendFile(t, "frontend/app.js")
	start := strings.Index(source, "function renderVersionDialog(")
	end := strings.Index(source[start:], "function openVersionDialog(") + start
	card := strings.Index(source, "function renderUpdate(")
	cardEnd := strings.Index(source[card:], "window.setInterval(") + card
	program := `const assert=require('node:assert/strict');
const nodes=new Map();
function $(id){if(!nodes.has(id))nodes.set(id,{hidden:false,textContent:'',classList:{toggle(){}},querySelectorAll(){return []}});return nodes.get(id)}
const t=(key,args)=>key+(args?JSON.stringify(args):'');const bytes=()=>'';const updateDownloadLine=()=>'';
` + source[start:end] + source[card:cardEnd] + `
const store={client_version:'0.1.18+r1882',update:{state:'store',channel:'store',current_version:'0.1.18.1882'}};
renderVersionDialog(store);renderUpdate(store);
assert.equal($('#version-channel').textContent,'version.storeChannel');
assert.equal($('#version-release').textContent,'0.1.18.1882');
assert.equal($('#version-status').textContent,'version.storeSummary');
assert.equal($('#version-update-actions').hidden,true);
assert.equal($('#version-restart-actions').hidden,true);
assert.equal($('#version-store-actions').hidden,false);
assert.equal($('#update-card').hidden,true);
const msi={update:{state:'available',channel:'alpha',current_version:'1',available_version:'2'}};
renderVersionDialog(msi);renderUpdate(msi);
assert.equal($('#version-channel').textContent,'alpha');
assert.equal($('#version-store-actions').hidden,true);
assert.equal($('#version-update-actions').hidden,false);
assert.equal($('#update-actions').hidden,false);
renderVersionDialog({});renderUpdate({});assert.equal($('#version-channel').textContent,'version.unknownChannel');assert.equal($('#update-card').hidden,true);
`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(program)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestStoreLinkUsesOnlyThePublishedProductAddress(t *testing.T) {
	raw, err := os.ReadFile("../../packaging/windows/store-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		StoreURL string `json:"storeUrl"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil || identity.StoreURL == "" {
		t.Fatal("invalid Store identity", err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source := embeddedFrontendFile(t, "frontend/app.js")
	quoted, _ := json.Marshal(identity.StoreURL)
	if !strings.Contains(source, "const FILEES_STORE = "+string(quoted)+";") {
		t.Fatal("Store link differs from the package product identity")
	}
	start := strings.Index(source, "function openFixedExternalLink(")
	end := strings.Index(source[start:], "\n}") + start + 2
	program := `const assert=require('node:assert/strict');const calls=[];
const FILEES_SITE='https://filees.space/';const FILEES_STORE='https://apps.microsoft.com/detail/9P0J1BRL5K77';
const Browser={OpenURL(url){calls.push(url);return Promise.resolve();}};
` + source[start:end] + `
const click=openFixedExternalLink;
const event=url=>({target:{closest(){return {dataset:{externalUrl:url}}}}});
click(event(FILEES_STORE));click(event(FILEES_SITE));
click(event(FILEES_STORE+'?redirect=elsewhere'));click(event('https://example.com'));click(event('file:///C:/private'));
assert.deepEqual(calls,[FILEES_STORE,FILEES_SITE]);`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(program)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	markup := embeddedFrontendFile(t, "frontend/index.html")
	if strings.Count(markup, `data-external-url="https://apps.microsoft.com/detail/9P0J1BRL5K77"`) != 1 {
		t.Fatal("only the version dialog should contain the fixed Store link")
	}
	if !strings.Contains(source, `$("#version-store-actions").addEventListener("click", openFixedExternalLink)`) {
		t.Fatal("Store button has no handler on its actual container")
	}
	for _, id := range []string{"version-store-actions"} {
		if !strings.Contains(markup, `id="`+id+`"`) {
			t.Fatal(id)
		}
	}
}
