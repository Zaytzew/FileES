package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAnnouncementBannerAndExplicitAckQueue(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	script := embeddedFrontendFile(t, "frontend/app.js")
	start := strings.Index(script, "function unreadAnnouncements(")
	end := strings.Index(script, "// renderDetached shows")
	if start < 0 || end < start {
		t.Fatal("announcement functions missing")
	}
	program := frontendI18NTestPrelude(t) + `
const assert = require("node:assert/strict");
const nodes = new Map();
const $ = id => { if(!nodes.has(id)) nodes.set(id, {hidden:false, disabled:false, textContent:"", html:"", isConnected:true, classList:{toggle(){}}, focus(){}}); return nodes.get(id); };
const replaceHTMLIfChanged = (node, html) => {node.html=html;};
const plural = (n, one, few, many) => n===1 ? one : n===2 ? few : many;
const escapeHTML = v => String(v ?? "").replaceAll("<","&lt;");
const repoIcons = {publish:"speaker"};
const shortDateTime = v => v;
const window = {setTimeout(){}};
let currentSnapshot = {repositories:[],servers:[],notices:[
 {id:"a",title:"Pierwsze",acked:false,can_ack:true,revision:2},
 {id:"b",title:"Drugie",acked:false,can_ack:true,revision:3}
]};
let selectedAnnouncementID="", announcementAckPending="", announcementReturnFocus=null;
let accepted=true;
const calls=[], toasts=[];
const GUIService = {Trigger:async request=>{calls.push(request); return {accepted,code:"action_unavailable"};}};
const actionErrors={action_unavailable:"Nie można teraz potwierdzić"};
const showToast = toast => toasts.push(toast);
` + script[start:end] + `
(async()=>{
renderAnnouncementBanner(currentSnapshot);
assert.equal($("#announcement-banner").hidden,false);
assert.match($("#announcement-banner-count").textContent,/2 nieprzeczytane/);
openNewestUnreadAnnouncement($("#open-announcements"));
assert.equal(selectedAnnouncementID,"a");
closeAnnouncement();
assert.equal(calls.length,0); // reading, Escape and close are not ACK
assert.equal(currentSnapshot.notices[0].acked,false);
openNewestUnreadAnnouncement();
await acknowledgeAnnouncement();
assert.deepEqual(calls,[{kind:"ack_notice",notice_id:"a"}]);
assert.equal(selectedAnnouncementID,"a"); // queue acceptance is not durable ACK
assert.equal(announcementAckPending,"a");
assert.equal(currentSnapshot.notices[0].acked,false);
currentSnapshot.notices[0].acked=true;
currentSnapshot.notices[0].can_ack=false;
renderAnnouncementBanner(currentSnapshot);
renderAnnouncementDialog(currentSnapshot);
assert.equal(selectedAnnouncementID,"b"); // advance only after daemon receipt
assert.match($("#announcement-banner-count").textContent,/1 nieprzeczytane/);
accepted=false;
await acknowledgeAnnouncement();
assert.equal(selectedAnnouncementID,"b");
assert.equal(announcementAckPending,"");
assert.equal(toasts.length,1);
assert.equal($("#announcement-banner").hidden,false);
accepted=true;
await acknowledgeAnnouncement();
currentSnapshot.notices[1].acked=true;
currentSnapshot.notices[1].can_ack=false;
renderAnnouncementBanner(currentSnapshot); renderAnnouncementDialog(currentSnapshot);
assert.equal($("#announcement-banner").hidden,true);
assert.equal($("#announcement-overlay").hidden,true);
assert.equal(selectedAnnouncementID,"");
currentSnapshot.notices=Array.from({length:7},(_,i)=>({id:"read"+i,title:"Read"+i,acked:true}));
currentSnapshot.notices.push({id:"old-unread",title:"Nie wolno schować starszego",acked:false,can_ack:true});
renderShouts(currentSnapshot);
assert.match($("#shouts").html,/old-unread/);
assert.equal(($("#shouts").html.match(/data-notice-id=/g)||[]).length,6);
currentSnapshot.notices=Array.from({length:9},(_,i)=>({id:"unread"+i,title:"Unread"+i,acked:false,can_ack:true}));
renderShouts(currentSnapshot);
assert.equal(($("#shouts").html.match(/data-notice-id=/g)||[]).length,9);
openNewestUnreadAnnouncement(); openNextUnreadAnnouncement();
assert.equal(selectedAnnouncementID,"unread1");
assert.equal(calls.length,3); // navigation never acknowledges
currentSnapshot.repositories=[{id:"r",server_id:"s",display_name:"<Test>",intent_resolution_required:true}];
renderAnnouncementBanner(currentSnapshot);
assert.equal($("#intent-alerts").hidden,false);
assert.match($("#intent-alerts").html,/&lt;Test>/);
assert.match($("#intent-alerts").html,/data-action="settings"/);
assert.match($("#hero-title").html,/na Twoją decyzję/);
const alertHTML=$("#intent-alerts").html;
for(let tick=0;tick<10;tick++) renderAnnouncementBanner(currentSnapshot);
assert.equal($("#intent-alerts").html,alertHTML);
assert.equal(calls.length,3); // ticks do not acknowledge, publish or reopen anything
assert.equal($("#announcement-banner").hidden,false); // shouts coexist
currentSnapshot.repositories[0].intent_resolution_required=false;
renderAnnouncementBanner(currentSnapshot);
assert.equal($("#intent-alerts").hidden,true);
assert.equal($("#announcement-banner").hidden,false);
})().catch(error=>{console.error(error);process.exitCode=1;});
`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(program)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("announcement JS: %v\n%s", err, out)
	}
}

func TestAnnouncementFrontendContainsCompletePublishAndAckFlow(t *testing.T) {
	index := embeddedFrontendFile(t, "frontend/index.html")
	script := embeddedFrontendFile(t, "frontend/app.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")

	for _, required := range []string{`id="shouts-card"`, `id="shouts"`, `Ostatnie ogłoszenia`, `id="announcement-overlay"`, `Potwierdź odczyt`} {
		if !strings.Contains(index, required) {
			t.Fatalf("frontend index does not contain %q", required)
		}
	}
	for _, required := range []string{`repoAction("publish"`, `kind: "ack_notice"`, `notice_id: notice.id`, `renderShouts(snapshot)`, `openAnnouncement(button.dataset.noticeId`, `Events.On("filees:open-announcement"`} {
		if !strings.Contains(script, required) {
			t.Fatalf("frontend script does not contain %q", required)
		}
	}
	for _, required := range []string{".shout-list", ".shout-row", "#shouts-card.has-unread", "announcement-panel-alert", ".announcement-dialog"} {
		if !strings.Contains(styles, required) {
			t.Fatalf("frontend styles do not contain %q", required)
		}
	}
	for _, forbidden := range []string{`Ostatnie /shouts/`, `Shout`, `shouting commit`} {
		if strings.Contains(index, forbidden) {
			t.Fatalf("public frontend copy leaks internal term %q", forbidden)
		}
	}
}

func embeddedFrontendFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := frontend.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
