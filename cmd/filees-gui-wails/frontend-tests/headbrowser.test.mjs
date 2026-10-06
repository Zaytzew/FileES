import test from "node:test";
import assert from "node:assert/strict";
import { entryType, sortEntries, copyStatusKey, releaseAllowed } from "../frontend/headbrowser-view.js";

test("a file's badge is its extension, never a guess from content", () => {
  assert.equal(entryType("model.blend"), "BLEND");
  assert.equal(entryType("Plan.v2.PSD"), "PSD");
  assert.equal(entryType("README"), "—");
  assert.equal(entryType(".hidden"), "—");
  assert.equal(entryType("trailing."), "—");
});

test("folders come first, then files, in natural order", () => {
  const sorted = sortEntries([
    { name: "b10.txt", kind: "file" }, { name: "zeta", kind: "dir" },
    { name: "b2.txt", kind: "file" }, { name: "Alpha", kind: "dir" },
  ], "pl");
  assert.deepEqual(sorted.map(entry => entry.name), ["Alpha", "zeta", "b2.txt", "b10.txt"]);
});

test("a sparse copy is never presented as the whole folder", () => {
  assert.equal(copyStatusKey({ attached: false }), "headBrowser.statusNone");
  assert.equal(copyStatusKey({ attached: true, sparse: true }), "headBrowser.statusSparse");
  assert.equal(copyStatusKey({ attached: true, sparse: false }), "headBrowser.statusFull");
});

test("release requires a local file and the current daemon capability", () => {
  const repo = { attached: true, sparse: true, can_release: true };
  const file = { local: true, kind: "file" };
  assert.equal(releaseAllowed(repo, file), true);
  for (const field of ["attached", "sparse", "can_release"]) {
    assert.equal(releaseAllowed({ ...repo, [field]: false }, file), false);
  }
  assert.equal(releaseAllowed(repo, { ...file, local: false }), false);
  assert.equal(releaseAllowed(repo, { ...file, kind: "dir" }), false);
  assert.equal(releaseAllowed(null, file), false);
  assert.equal(releaseAllowed(repo, null), false);
});
