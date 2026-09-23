import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { renameOffered } from "../frontend/rename-entry.js";
import { languages } from "../frontend/i18n.js";

const read = path => readFileSync(new URL(path, import.meta.url), "utf8");

test("display name is offered on the folder's own view, and only while the form is closed", () => {
  assert.equal(renameOffered(false, null), true);
  // Shares, grants, uploads, quarantine and the shelf replace the action list;
  // renaming from there would edit a folder the view is not about.
  assert.equal(renameOffered(true, null), false);
  // The form is already open: a second entry to it would only reset the input.
  assert.equal(renameOffered(false, {name: "ARCHIWUM"}), false);
  assert.equal(renameOffered(true, {name: "ARCHIWUM"}), false);
});

// The row in the list and the button beside the heading must appear and vanish
// together. The rule is one function; this checks that both places actually
// consult it, because a second, inline copy of the condition is exactly how the
// two would drift apart.
test("the list row and the heading button follow the same rule", () => {
  const source = read("../frontend/repository.js");
  assert.match(source, /const offerRename = renameOffered\(detailMode, renaming\);/);
  assert.match(source, /\$\("#rename-inline"\)\.hidden = !offerRename;/);
  assert.match(source, /if \(offerRename\) \$\("#repository-actions"\)\.innerHTML \+= `<button class="action-row" type="button" data-rename-view>/);
  assert.match(source, /\$\("#rename-inline"\)\.addEventListener\("click", openRename\);/);
});

test("the heading button is labelled in every language and starts hidden", () => {
  const html = read("../frontend/repository.html");
  const button = html.match(/<button id="rename-inline"[^>]*>/)?.[0];
  assert.ok(button, "heading button missing");
  // Hidden until the first snapshot decides; otherwise it flashes on a view
  // that turns out to be shares or grants.
  assert.match(button, /\shidden(\s|>)/);
  assert.match(button, /data-i18n-title="rename\.row"/);
  assert.match(button, /data-i18n-aria-label="rename\.row"/);
  // An icon-only control has no name without its label, so every catalogue
  // must carry it rather than fall back.
  for (const language of languages) {
    assert.equal(typeof language.messages["rename.row"], "string", language.code);
    assert.ok(language.messages["rename.row"].trim(), language.code);
  }
});
