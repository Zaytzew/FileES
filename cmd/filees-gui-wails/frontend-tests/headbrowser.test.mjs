import test from "node:test";
import assert from "node:assert/strict";
import { entryType, sortEntries, copyStatusKey, releaseOffered, releaseAllowed, materializeAllowed, availabilityKey, moveAllowed } from "../frontend/headbrowser-view.js";

test("folder move is offered only by an attached writable virtual-folder daemon",()=>{
 const repo={can_move:true,attached:true,sparse:true,access:"rw"},dir={kind:"dir"};
 assert.equal(moveAllowed(repo,dir),true);
 for(const field of ["can_move","attached","sparse"]) assert.equal(moveAllowed({...repo,[field]:false},dir),false);
 assert.equal(moveAllowed({...repo,access:"r"},dir),false);
 assert.equal(moveAllowed(repo,{kind:"file"}),false);
});

test("directory badges describe content, never just the local skeleton", () => {
  for (const value of ["remote", "hybrid", "local"]) {
    assert.equal(availabilityKey({ kind: "dir", local: true, availability: value }), `headBrowser.availability.${value}`);
  }
  assert.equal(availabilityKey({ kind: "dir", local: true }), "");
  assert.equal(availabilityKey({ kind: "dir", local: true, availability: "invalid" }), "");
  assert.equal(availabilityKey({ kind: "file", local: true }), "headBrowser.availability.local");
  assert.equal(availabilityKey({ kind: "file", local: false }), "headBrowser.availability.remote");
  assert.equal(availabilityKey(null), "");
});

test("a file's badge is its extension, never a guess from content", () => {
  assert.equal(entryType("model.blend"), "BLEND");
  assert.equal(entryType("Plan.v2.PSD"), "PSD");
  assert.equal(entryType("README"), "—");
  assert.equal(entryType(".hidden"), "—");
  assert.equal(entryType("trailing."), "—");
});

test("folder actions require the batch capability even on a partly local folder", () => {
  const repo = { attached: true, sparse: true, can_release: true, can_batch: true };
  const folder = { kind: "dir", local: true, availability: "hybrid" };
  assert.equal(releaseAllowed(repo, folder), true);
  assert.equal(materializeAllowed(repo, folder), true);
  assert.equal(releaseAllowed({ ...repo, can_batch: false }, folder), false);
  assert.equal(materializeAllowed({ ...repo, can_batch: false }, folder), false);
  assert.equal(materializeAllowed(repo, { kind: "file", local: true }), false);
});

test("remote and unknown folders keep a disabled release button", () => {
  const repo = { attached: true, sparse: true, can_release: true, can_batch: true };
  for (const availability of ["remote", "unknown", undefined, "invalid"]) {
    const folder = { kind: "dir", local: true, availability };
    assert.equal(releaseOffered(repo, folder), true);
    assert.equal(releaseAllowed(repo, folder), false);
  }
  for (const availability of ["hybrid", "local"]) {
    assert.equal(releaseAllowed(repo, { kind: "dir", local: true, availability }), true);
  }
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
