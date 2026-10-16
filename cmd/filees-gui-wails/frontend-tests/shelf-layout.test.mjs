import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { shelvesFor, unparentedShelves } from "../frontend/shelf-layout.js";

// Source wiring guard, not a browser interaction acceptance.
test("nested shelves use the existing fold state across projection refresh", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  assert.ok(source.includes('JSON.stringify([repo.server_id, repo.id, "shelves"])'));
  assert.ok(source.includes('<details class="parent-shelves idle-group" data-idle-key='));
  assert.ok(source.includes('expandedIdleGroups.has(shelfKey) ? "open" : ""'));
});

test("shelves follow explicit parent ID, never name or another server", () => {
  const parent = {id:"parent",server_id:"one",display_name:"Renamed project"};
  const child = {id:"shelf",server_id:"one",parent_repo_id:"parent",purpose:"upload_shelf"};
  const orphan = {id:"orphan",server_id:"one",display_name:"Renamed project",purpose:"upload_shelf"};
  const other = {...child,id:"other",server_id:"two"};
  const repos = [parent,child,orphan,other];
  assert.deepEqual(shelvesFor(parent,repos),[child]);
  assert.deepEqual(unparentedShelves(repos),[orphan,other]);
  assert.deepEqual(shelvesFor(child,repos),[]);
  assert.deepEqual(unparentedShelves([{...parent,server_deleted:true},child]),[child]);
});

test("shelf browser renders its own name in both headers without reusing repository context", () => {
  const source = readFileSync(new URL("../frontend/repository.js", import.meta.url), "utf8");
  const begin = source.indexOf("function render(snapshot)"), end = source.indexOf("\n}", begin) + 2;
  const nodes = new Map();
  const node = selector => {
    if (!nodes.has(selector)) nodes.set(selector, {textContent: "", innerHTML: "", hidden: false});
    return nodes.get(selector);
  };
  const render = runInNewContext(`let currentSnapshot = null, renaming = null;\n${source.slice(begin, end)}\nrender`, {
    $: node, t: key => key, labelHTML: key => key, escapeHTML: value => String(value),
    renderProblem() {}, shelfCard: () => "<row>", renameOffered: () => false, readRepoView: () => ({}),
    repoSection: () => "active", canArchive: () => false,
    window: {requestAnimationFrame() {}},
  });
  const context = {server_id: "server", repo_id: "opaque-uuid"};
  for (const name of [undefined, "Parent repository", "Previous unrelated repository"]) {
    const snapshot = {revision: 1, mode: "shelf", context: {...context, name}, shelf_name: "Półka Łódź <literal>"};
    const before = JSON.stringify(snapshot);
    render(snapshot);
    assert.equal(node("#window-context").textContent, snapshot.shelf_name);
    assert.equal(node("#repository-name").textContent, snapshot.shelf_name);
    assert.equal(node("#shelf-heading").textContent, `shelf.title — ${snapshot.shelf_name}`);
    assert.equal(JSON.stringify(snapshot), before, "presentation must not change operation identity");
  }
  render({revision: 2, mode: "shelf", context});
  assert.equal(node("#shelf-import-all").disabled, true);
  assert.equal(node("#shelf-import-unavailable").hidden, false);
  render({revision: 3, mode: "shelf", context, shelf_can_import:true, shelf:[{upload_id:"one"}]});
  assert.equal(node("#shelf-import-all").disabled, false);
  assert.equal(node("#shelf-import-unavailable").hidden, true);
  assert.equal(node("#repository-name").textContent, "shelf.title");
  assert.equal(node("#window-context").textContent, "shelf.title");
  for (const mode of ["actions", "uploads", "shares", "quarantine", "grants"]) {
    render({revision: 3, mode, context: {...context, name: "Project"}, shelf_name: "Ignore outside shelf"});
    assert.equal(node("#repository-name").textContent, "Project");
    assert.equal(node("#window-context").textContent, "Project");
  }
});
