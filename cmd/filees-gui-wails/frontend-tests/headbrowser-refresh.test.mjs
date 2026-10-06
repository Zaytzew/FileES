import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import * as view from "../frontend/headbrowser-view.js";

test("returning from Explorer refreshes content without clearing rows or overlapping calls", async () => {
  const nodes = new Map();
  const node = name => {
    if (!nodes.has(name)) nodes.set(name, { innerHTML: "", addEventListener() {}, append() {}, appendChild() {} });
    return nodes.get(name);
  };
  const events = new Map();
  let requests = 0, complete, fail;
  const HeadBrowser = {
    Context: () => Promise.resolve({}),
    List: () => { requests++; return new Promise((resolve, reject) => { complete = resolve; fail = reject; }); },
  };
  const source = readFileSync(new URL("../frontend/headbrowser.js", import.meta.url), "utf8").replace(/^import .*;\r?\n/gm, "");
  const sandbox = {
    ...view, HeadBrowser, Events: { On() {} }, Window: {},
    initializeTheme() {}, initializeLanguage() {}, t: key => key, getLocale: () => "pl",
    formatBytes: () => "", breadcrumb: () => [], joinPath: (a,b) => a ? `${a}/${b}` : b, parentPath: () => "",
    document: { querySelector: node, createElement: () => ({ append() {} }), body: { classList: { toggle() {} } } },
    window: { addEventListener: (name, fn) => events.set(name, fn), setTimeout() {} },
  };
  runInNewContext(source + "\nglobalThis.browser = { state, renderEntries, list };", sandbox);
  await Promise.resolve();
  const { state, renderEntries, list } = sandbox.browser;
  state.repo = { attached: true, sparse: true };
  state.entries = [{ name: "folder", kind: "dir", local: true, availability: "hybrid" }];
  renderEntries();
  const before = node("#hb-entries").innerHTML;
  const refresh = events.get("focus")();
  events.get("focus")();
  assert.equal(requests, 1);
  assert.equal(node("#hb-entries").innerHTML, before);
  complete({ path: "", entries: [{ name: "folder", kind: "dir", local: true, availability: "remote" }] });
  await refresh;
  assert.match(node("#hb-entries").innerHTML, /availability.remote/);
  state.busy = "headBrowser.releasing";
  events.get("focus")();
  assert.equal(requests, 1);
  state.busy = "";
  const navigation = list("nested");
  assert.match(node("#hb-entries").innerHTML, /hb-spinner/);
  assert.doesNotMatch(node("#hb-entries").innerHTML, /availability.remote/);
  complete({ path: "nested", entries: [] });
  await navigation;
  state.entries = [{ name: "file", kind: "file", local: true, availability: "local" }];
  const failedRefresh = events.get("focus")();
  fail(new Error("offline"));
  await failedRefresh;
  assert.doesNotMatch(node("#hb-entries").innerHTML, /availability.local/);
  assert.equal(state.entries.length, 1);
});
