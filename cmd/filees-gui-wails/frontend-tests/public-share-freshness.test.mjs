import test from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";

test("stale public shares do not claim a connected daemon is offline", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const render = source.slice(source.indexOf("function renderMetrics("), source.indexOf("function plural("));
  const nodes = new Map();
  const get = key => { if (!nodes.has(key)) nodes.set(key, {dataset:{}}); return nodes.get(key); };
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
