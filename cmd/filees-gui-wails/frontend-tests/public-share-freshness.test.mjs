import test from "node:test";
import {queueSummary} from "../frontend/queue-state.js";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";

test("reservation card uses the same pending count as the header and stops on failure", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const extract = name => { const start = source.indexOf(`function ${name}(`); return start < 0 ? "" : source.slice(start, source.indexOf("\n}", start) + 2); };
  const nodes = new Map();
  const get = key => {
    if (!nodes.has(key)) nodes.set(key, {dataset:{}, setAttribute(name, value) { this[name] = value; }});
    return nodes.get(key);
  };
  const context = {$:get, t:key=>key, escapeHTML:String, replaceHTMLIfChanged:(node, html)=>node.innerHTML=html};
  runInNewContext(["countWaitsForServer", "renderWaitingCount", "renderReservations"].map(extract).join("\n"), context);
  const snapshot = {connected:true, projection:{state:"server_unverified"},
    reservation_status:{state:"partial", unavailable:[], offline:[], stale:[]}};
  const count = get("#reservations-count");
  const css = readFileSync(new URL("../frontend/app.css", import.meta.url), "utf8");
  assert.match(css, /\.count-badge \.metric-spinner\s*\{[^}]*display:inline-block;[^}]*width:12px;[^}]*height:12px;/);
  for (const state of ["server_unverified", "refreshing"]) {
    snapshot.projection.state = state;
    context.renderReservations(snapshot);
    assert.match(count.innerHTML, /^0\+<span class="action-spinner metric-spinner"/);
    assert.equal(count["aria-busy"], "true");
    assert.equal(count["aria-label"], "fresh.refreshing");
    assert.equal(get("#reservations-card").hidden, false);
  }
  for (const state of ["current", "server_unavailable", "daemon_offline"]) {
    snapshot.projection.state = state;
    snapshot.connected = state !== "daemon_offline";
    context.renderReservations(snapshot);
    assert.equal(count.innerHTML, "");
    assert.equal(count.textContent, "0+?");
    assert.equal(count["aria-busy"], "false");
    assert.equal(count.title, "");
  }
  snapshot.connected = true;
  snapshot.projection.state = "refreshing";
  snapshot.servers = [{health:"server_unavailable"}];
  context.renderReservations(snapshot);
  assert.equal(count.innerHTML, "");
  snapshot.servers = [];
  snapshot.reservation_status.state = "complete";
  context.renderReservations(snapshot);
  assert.equal(count.innerHTML, "");
  assert.equal(count.textContent, 0);
  assert.equal(count["aria-busy"], "false");
  assert.equal(get("#reservations-card").hidden, true);
});

test("stale public shares do not claim a connected daemon is offline", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const render = source.slice(source.indexOf("function renderMetrics("), source.indexOf("function plural("));
  const nodes = new Map();
  const get = key => { if (!nodes.has(key)) nodes.set(key, {dataset:{}, setAttribute(name, value) { this[name] = value; }}); return nodes.get(key); };
  const context = {queueSummary, $:get, t:key=>key, tn:key=>key, bytes:()=>""};
  runInNewContext(render, context);
  const snapshot = {connected:true, repos:[], public_shares_known:true,
    reservation_status:{state:"current"}, public_shares:[{state:"active",stale:true}]};
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares").textContent, 1);
  assert.equal(get("#metric-public-shares-note").textContent, "summary.lastKnownUnverified");
  snapshot.connected=false;
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares-note").textContent, "summary.lastKnown");
  snapshot.errors = [{severity:"INFO",message_key:"commit.recovery_resolved"},{severity:"WARN"}];
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-attention").textContent, 1);
  snapshot.connected=true; snapshot.public_shares[0].stale=false;
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares").textContent, 1);
  assert.equal(get("#metric-public-shares-note").textContent, "count.links");
});

test("public share spinner only waits for an unknown inventory and keeps known counts calm", () => {
 const source=readFileSync(new URL("../frontend/app.js",import.meta.url),"utf8");
 const nodes=new Map(); const get=key=>{if(!nodes.has(key))nodes.set(key,{dataset:{},setAttribute(name,value){this[name]=value}});return nodes.get(key)};
 const ctx={queueSummary,$:get,t:key=>key,tn:key=>key,bytes:()=>""};
 runInNewContext(source.slice(source.indexOf("function renderMetrics("),source.indexOf("function plural(")),ctx);
 const snapshot={connected:true,projection:{state:"current"},public_shares_known:false,public_shares_loading:true,reservation_status:{state:"complete"}};
 ctx.renderMetrics(snapshot);
 assert.equal(get("#metric-public-shares")["aria-busy"],"true");
 assert.match(get("#metric-public-shares").innerHTML,/metric-spinner/);
 assert.equal(get("#metric-public-shares-note").textContent,"fresh.refreshing");
 snapshot.public_shares_known=true; snapshot.public_shares_stale=true; snapshot.public_shares=[{state:"active",stale:true}];
 ctx.renderMetrics(snapshot);
 assert.equal(get("#metric-public-shares")["aria-busy"],"false");
 assert.equal(get("#metric-public-shares").textContent,1);
 assert.equal(get("#metric-public-shares-note").textContent,"summary.lastKnownUnverified");
 snapshot.public_shares_stale=false; snapshot.public_shares[0].stale=false; ctx.renderMetrics(snapshot);
 assert.equal(get("#metric-public-shares-note").textContent,"count.links");
 snapshot.public_shares_stale=true;
 snapshot.public_shares_loading=false; ctx.renderMetrics(snapshot);
 assert.equal(get("#metric-public-shares").textContent,1);
 assert.equal(get("#metric-public-shares")["aria-busy"],"false");
 assert.equal(get("#metric-public-shares-note").textContent,"summary.lastKnownUnverified");
 snapshot.connected=false; snapshot.public_shares_loading=true; ctx.renderMetrics(snapshot);
 assert.equal(get("#metric-public-shares")["aria-busy"],"false");
 snapshot.connected=true; snapshot.public_shares_loading=false; snapshot.public_shares=[]; ctx.renderMetrics(snapshot);
 assert.equal(get("#metric-public-shares").textContent,0);
 assert.equal(get("#metric-public-shares-note").textContent,"summary.lastKnownUnverified");
});

test("unknown metrics spin only while the projection waits, then settle to data or unavailable", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const render = source.slice(source.indexOf("function renderMetrics("), source.indexOf("function plural("));
  const nodes = new Map();
  const get = key => {
    if (!nodes.has(key)) nodes.set(key, {dataset:{}, setAttribute(name, value) { this[name] = value; }});
    return nodes.get(key);
  };
  const context = {queueSummary, $:get, t:key=>key, tn:key=>key, bytes:()=>""};
  runInNewContext(render, context);
  const snapshot = {connected:true, projection:{state:"server_unverified"},
    public_shares_known:false, reservations:[{}],
    reservation_status:{state:"partial", unavailable:[{}]}};
  for (const state of ["server_unverified", "refreshing"]) {
    snapshot.projection.state = state;
    context.renderMetrics(snapshot);
    for (const id of ["#metric-public-shares", "#metric-reservations"]) {
      assert.match(get(id).innerHTML, /action-spinner metric-spinner/);
      assert.equal(get(id)["aria-busy"], "true");
      assert.equal(get(id)["aria-label"], "fresh.refreshing");
    }
    assert.match(get("#metric-reservations").innerHTML, /^1\+/);
  }
  for (const state of ["current", "server_unavailable", "daemon_offline"]) {
    snapshot.projection.state = state;
    snapshot.connected = state !== "daemon_offline";
    context.renderMetrics(snapshot);
    assert.equal(get("#metric-public-shares").innerHTML, "");
    assert.equal(get("#metric-public-shares").textContent, "?");
    assert.equal(get("#metric-public-shares")["aria-busy"], "false");
    assert.equal(get("#metric-reservations").innerHTML, "");
  }
  snapshot.connected = true;
  snapshot.public_shares_known = true;
  snapshot.public_shares = [{state:"active"}];
  snapshot.reservation_status.state = "complete";
  snapshot.projection.state = "current";
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares").textContent, 1);
  assert.equal(get("#metric-reservations").textContent, 1);
  // A refresh must not replace already verified counts with a spinner.
  snapshot.projection.state = "refreshing";
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares").innerHTML, "");
  assert.equal(get("#metric-reservations").innerHTML, "");
  // A different server's pending first check must not hide a known failure.
  snapshot.public_shares_known = false;
  snapshot.servers = [{health:"server_unavailable"}];
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares").innerHTML, "");
});
