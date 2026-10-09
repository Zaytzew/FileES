import test from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";

test("stale public shares do not claim a connected daemon is offline", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const render = source.slice(source.indexOf("function renderMetrics("), source.indexOf("function plural("));
  const nodes = new Map();
  const get = key => { if (!nodes.has(key)) nodes.set(key, {dataset:{}, setAttribute(name, value) { this[name] = value; }}); return nodes.get(key); };
  const context = {$:get, t:key=>key, tn:key=>key, bytes:()=>""};
  runInNewContext(render, context);
  const snapshot = {connected:true, repos:[], public_shares_known:true,
    reservation_status:{state:"current"}, public_shares:[{state:"active",stale:true}]};
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares").textContent, "?");
  assert.equal(get("#metric-public-shares-note").textContent, "summary.lastKnownUnverified");
  snapshot.connected=false;
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares-note").textContent, "summary.lastKnown");
  snapshot.connected=true; snapshot.public_shares[0].stale=false;
  context.renderMetrics(snapshot);
  assert.equal(get("#metric-public-shares").textContent, 1);
  assert.equal(get("#metric-public-shares-note").textContent, "count.links");
});

test("unknown metrics spin only while the projection waits, then settle to data or unavailable", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const render = source.slice(source.indexOf("function renderMetrics("), source.indexOf("function plural("));
  const nodes = new Map();
  const get = key => {
    if (!nodes.has(key)) nodes.set(key, {dataset:{}, setAttribute(name, value) { this[name] = value; }});
    return nodes.get(key);
  };
  const context = {$:get, t:key=>key, tn:key=>key, bytes:()=>""};
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
