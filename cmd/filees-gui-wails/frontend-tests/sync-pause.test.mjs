import test from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";

test("manual and editor pause keep radar stopped until both are cleared", () => {
 const source=readFileSync(new URL("../frontend/app.js",import.meta.url),"utf8");
 const render=source.slice(source.indexOf("function renderSyncPause("),source.indexOf("function renderConnection("));
 const nodes=new Map();
 const get=id=>{if(!nodes.has(id))nodes.set(id,{dataset:{},attrs:{},classList:{toggle(key,value){this[key]=value}},setAttribute(k,v){this.attrs[k]=v}});return nodes.get(id)};
 const context={$:get,t:key=>key};runInNewContext(render,context);
 const snapshot={connected:true,stale:false,capabilities:["sync.pause"],sync_pause:{manual:true,draft:true}};
 context.renderSyncPause(snapshot);
 assert.equal(get("#sync-pause").dataset.action,"resume_sync");
 assert.equal(get("#sync-pause").attrs["aria-pressed"],"true");
 assert.equal(get("#sync-pause-status").textContent,"pause.both");
 assert.equal(get("#pulse-card").classList["is-paused"],true);
 snapshot.sync_pause.manual=false;context.renderSyncPause(snapshot);
 assert.equal(get("#sync-pause").attrs["aria-pressed"],"false");
 assert.equal(get("#sync-pause").dataset.action,"pause_sync");
 assert.equal(get("#pulse-card").classList["is-paused"],true);
 snapshot.sync_pause.draft=false;context.renderSyncPause(snapshot);
 assert.equal(get("#pulse-card").classList["is-paused"],false);
 assert.equal(get("#sync-pause-status").hidden,true);
 snapshot.connected=false;context.renderSyncPause(snapshot);assert.equal(get("#sync-pause").disabled,true);
 snapshot.connected=true;snapshot.capabilities=[];context.renderSyncPause(snapshot);assert.equal(get("#sync-pause").disabled,true);
 const css=readFileSync(new URL("../frontend/app.css",import.meta.url),"utf8");
 assert.match(css,/#pulse-card\.is-paused \.pulse-orbit\s*\{\s*animation-play-state: paused;/);
});

test("pause uses the shared header control with a red signal in both states", () => {
 const html=readFileSync(new URL("../frontend/index.html",import.meta.url),"utf8");
 const css=readFileSync(new URL("../frontend/app.css",import.meta.url),"utf8");
 assert.match(html, /id="sync-pause" class="icon-button pair-button sync-pause-button"/);
 assert.match(css, /\.topbar #sync-pause\s*\{[^}]*min-width:108px;[^}]*color:var\(--red\)/);
 assert.match(css, /\.topbar #sync-pause\[aria-pressed="true"\]\s*\{[^}]*border-color:var\(--red\)/);
});
