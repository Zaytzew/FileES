import test from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";
import {languages, translate} from "../frontend/i18n.js";

const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
const catalogues = Object.fromEntries(languages.map(language => [language.code, language.messages]));
function harness(language = "pl") {
  const start = source.indexOf("function pendingHolderLockRequests(");
  const end = source.indexOf("function unreadAnnouncements(");
  assert.ok(start >= 0 && end > start, "lock release dialog functions missing");
  const nodes = new Map(), calls = [], toasts = [], panelButtons = [];
  const $ = id => {
    if (!nodes.has(id)) nodes.set(id, {hidden: true, disabled: false, textContent: "", isConnected: true,
      focus() { this.focused = true; }, classList: {toggle() {}}});
    return nodes.get(id);
  };
  const context = {$, document: {querySelectorAll: () => panelButtons}, currentSnapshot: null, selectedLockReleaseID: "", lockReleaseReturnFocus: null,
    lockReleaseInFlight: new Set(), selectedAnnouncementID: "", shortDateTime: value => value,
    t: (key, args) => translate(catalogues, language, key, args),
    tn: (key, count) => translate(catalogues, language, key, {count}),
    GUIService: {Trigger: async request => {calls.push(request); return {accepted: true};}},
    showToast: toast => toasts.push(toast), actionErrors: {},
    renderReservations() {}, closeAnnouncement() { context.selectedAnnouncementID = ""; }};
  runInNewContext(source.slice(start, end), context);
  return {context, $, calls, toasts, panelButtons};
}
const request = (id, extra = {}) => ({id, role: "holder", state: "pending", path: "<plan>.dwg",
  repository: "Projekt", counterparty_realm_alias: "Projektanci", created_at: "2026-10-05T12:00:00Z",
  can_accept: true, can_dismiss: true, ...extra});
function snapshot(requests) { return {connected: true, stale: false, lock_release_requests: requests}; }

test("holder requests stay prominent until a confirmed terminal state, not opening or closing", () => {
  const {context: c, $, calls} = harness();
  c.currentSnapshot = snapshot([request("a"), request("b"), request("other", {role: "requester"}), request("old", {state: "accepted"})]);
  c.renderLockReleaseRequests(c.currentSnapshot);
  assert.equal($("#lock-release-banner").hidden, false);
  assert.match($("#lock-release-banner-count").textContent, /2/);
  c.openLockReleaseRequests($("#open-lock-release"));
  assert.equal(c.selectedLockReleaseID, "a");
  assert.equal($("#lock-release-path").textContent, "<plan>.dwg");
  assert.equal($("#lock-release-who").textContent, "Projektanci");
  c.openNextLockReleaseRequest(); assert.equal(c.selectedLockReleaseID, "b");
  c.closeLockReleaseRequests(); assert.equal(c.selectedLockReleaseID, "");
  assert.equal(calls.length, 0);
  c.renderLockReleaseRequests(c.currentSnapshot); assert.equal($("#lock-release-banner").hidden, false);
  c.openLockReleaseRequests(); // reopening simulates GUI reconstruction from the same projection
  for (let i = 0; i < 10; i++) c.renderLockReleaseRequests(c.currentSnapshot);
  assert.equal(c.selectedLockReleaseID, "a"); assert.equal(calls.length, 0);
  c.currentSnapshot.lock_release_requests[0].state = "dismissed";
  c.renderLockReleaseRequests(c.currentSnapshot); assert.equal(c.selectedLockReleaseID, "b");
  c.currentSnapshot.lock_release_requests[1].state = "expired";
  c.renderLockReleaseRequests(c.currentSnapshot);
  assert.equal($("#lock-release-banner").hidden, true);
  assert.equal($("#lock-release-overlay").hidden, true);
});

test("explicit decisions use the exact projected ID and queue acceptance does not clear the banner", async () => {
  const {context: c, $, calls, toasts} = harness();
  c.currentSnapshot = snapshot([request("a")]); c.openLockReleaseRequests();
  await c.decideLockRelease("a", "accept_lock_release");
  assert.deepEqual(JSON.parse(JSON.stringify(calls)), [{kind: "accept_lock_release", lock_release_request_id: "a"}]);
  assert.equal(c.currentSnapshot.lock_release_requests[0].state, "pending");
  assert.equal($("#lock-release-banner").hidden, false);
  c.GUIService.Trigger = async data => {calls.push(data); return {accepted: false, code: "action_stale"};};
  await c.decideLockRelease("a", "dismiss_lock_release");
  assert.equal(toasts.length, 1); assert.equal($("#lock-release-overlay").hidden, false);
  assert.equal($("#lock-release-dismiss").disabled, false);
  c.GUIService.Trigger = async () => {throw new Error("offline");};
  await c.decideLockRelease("a", "dismiss_lock_release"); assert.equal(toasts.length, 2);
  assert.equal(c.lockReleaseInFlight.size, 0);
});

test("offline, stale, foreign, terminal and unavailable actions cannot send a decision", async () => {
  const {context: c, $, calls} = harness();
  c.currentSnapshot = snapshot([request("a")]); c.openLockReleaseRequests();
  c.currentSnapshot.connected = false; c.renderLockReleaseRequests(c.currentSnapshot);
  assert.equal($("#lock-release-accept").disabled, true);
  assert.match($("#lock-release-status").textContent, /aktualny|połączen/i);
  await c.decideLockRelease("a", "accept_lock_release");
  c.currentSnapshot.connected = true; c.currentSnapshot.stale = true;
  await c.decideLockRelease("a", "accept_lock_release");
  c.currentSnapshot.stale = false;
  c.currentSnapshot.lock_release_requests[0].can_accept = false;
  await c.decideLockRelease("a", "accept_lock_release");
  await c.decideLockRelease("a", "unexpected");
  await c.decideLockRelease("missing", "dismiss_lock_release");
  c.currentSnapshot.lock_release_requests[0].role = "requester";
  await c.decideLockRelease("a", "dismiss_lock_release");
  c.currentSnapshot.lock_release_requests[0].role = "holder";
  c.currentSnapshot.lock_release_requests[0].state = "lock_gone";
  await c.decideLockRelease("a", "dismiss_lock_release"); assert.equal(calls.length, 0);
});

test("one decision in flight is shared between panel and dialog, even when the dialog closes", async () => {
  const {context: c, $, calls, panelButtons} = harness();
  for (const action of ["accept_lock_release", "dismiss_lock_release"]) panelButtons.push({
    dataset: {action}, disabled: false,
    closest: () => ({dataset: {lockReleaseRequestId: "a"}}),
  });
  c.currentSnapshot = snapshot([request("a")]); c.openLockReleaseRequests();
  assert.ok(panelButtons.every(button => !button.disabled));
  let finish;
  c.GUIService.Trigger = data => {calls.push(data); return new Promise(resolve => {finish = resolve;});};
  const first = c.decideLockRelease("a", "accept_lock_release");
  assert.equal($("#lock-release-accept").disabled, true);
  assert.ok(panelButtons.every(button => button.disabled));
  c.closeLockReleaseRequests(); c.openLockReleaseRequests();
  await c.decideLockRelease("a", "dismiss_lock_release"); assert.equal(calls.length, 1);
  finish({accepted: true}); await first;
  assert.equal($("#lock-release-accept").disabled, false);
  assert.ok(panelButtons.every(button => !button.disabled));
  c.currentSnapshot.stale = true; c.renderLockReleaseRequests(c.currentSnapshot);
  assert.ok(panelButtons.every(button => button.disabled));
});

test("every catalogue explains that closing is not a decision and plural counts render", () => {
  for (const language of languages) {
    const {context: c, $} = harness(language.code);
    c.currentSnapshot = snapshot([request("a")]); c.openLockReleaseRequests();
    assert.doesNotMatch($("#lock-release-status").textContent, /\[|\{count\}/);
    for (const count of [1, 2, 5, 22]) {
      assert.ok(translate(catalogues, language.code, "locks.requestsCount", {count}).includes(String(count)));
    }
    assert.ok(catalogues[language.code]["locks.requestsHelp"]);
  }
});

test("real dashboard wiring exposes the context, explicit decisions and keyboard dismissal", () => {
  const html = readFileSync(new URL("../frontend/index.html", import.meta.url), "utf8");
  const css = readFileSync(new URL("../frontend/app.css", import.meta.url), "utf8");
  for (const id of ["lock-release-banner", "open-lock-release", "lock-release-overlay", "lock-release-path", "lock-release-repository", "lock-release-who", "lock-release-time", "lock-release-dismiss", "lock-release-accept"]) {
    assert.ok(html.includes(`id="${id}"`), id);
  }
  assert.match(html, /aria-describedby="lock-release-status"/);
  assert.match(source, /renderReservations\(snapshot\);\s*renderLockReleaseRequests\(snapshot\);/);
  assert.match(source, /"#lock-release-accept"\)\.addEventListener\("click", \(\) => decideLockRelease\(selectedLockReleaseID, "accept_lock_release"\)/);
  assert.match(source, /"#lock-release-dismiss"\)\.addEventListener\("click", \(\) => decideLockRelease\(selectedLockReleaseID, "dismiss_lock_release"\)/);
  assert.match(source, /event.key === "Escape"\) \{ event.preventDefault\(\); closeLockReleaseRequests\(\); return;/);
  assert.match(source, /overlay.querySelectorAll\("button"\)/);
  assert.match(css, /#lock-release-banner:not\(\[hidden\]\)/);
  assert.match(css, /prefers-reduced-motion:reduce/);
});
