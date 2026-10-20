import test from "node:test";
import assert from "node:assert/strict";
import {anchorRuntimeMarkup} from "../frontend/anchor-runtime.js";
import {languages} from "../frontend/i18n.js";

const escape = v => String(v).replaceAll("<", "&lt;").replaceAll(">", "&gt;");
const t = (key, args={}) => key + JSON.stringify(args);
const repo = phase => ({attached: true, anchor_runtime: {phase, since: "2026-10-20T11:00:00Z", detail: "<script>error</script>"}});
test("provider preparation and failure remain visible, without invented percentages", () => {
  for (const phase of ["preparing", "connecting", "failed", "future"]) {
    const html = anchorRuntimeMarkup(repo(phase), t, escape, v => v);
    assert.match(html, new RegExp(`anchorRuntime.${phase === "future" ? "unknown" : phase}`));
    assert.match(html, /anchorRuntime.since/);
    assert.doesNotMatch(html, /<script>|\d+%/);
    if (phase === "failed") assert.match(html, /&lt;script&gt;error/);
  }
  assert.equal(anchorRuntimeMarkup(repo("ready"),t,escape,String), "");
  assert.equal(anchorRuntimeMarkup({...repo("failed"),attached:false},t,escape,String), "");
  assert.equal(anchorRuntimeMarkup({attached:true},t,escape,String), "");
});
test("provider preparation copy exists in all interface languages", () => {
  for (const language of languages) {
    for (const key of ["preparing","connecting","failed","unknown","since","waitHelp","failureHelp","diagnostic"]) {
      assert.ok(language.messages[`anchorRuntime.${key}`], `${language.code}: ${key}`);
    }
  }
});
