import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

test("folder spinner never borrows publication percentages", () => {
  const source = readFileSync(new URL("../frontend/progress-overlay.js", import.meta.url), "utf8")
    .replace(/^import .*;\r?\n/gm, "").replace("export function initializeProgressOverlay", "function initializeProgressOverlay");
  const sandbox = { t: key => key };
  runInNewContext(source + "\nglobalThis.inspect = { measure, setContext: value => context = value };", sandbox);
  sandbox.inspect.setContext({ snapshot: () => ({ repositories: [{ local_path: "D:/folder", publish_files_total: 100, publish_files_done: 70 }] }), bytes: String });
  for (const action of ["download", "release"]) {
    const result = sandbox.inspect.measure({ presentation_key: `progress.anchorFolder.${action}`, presentation_args: { path: "D:/folder" } });
    assert.equal(result.share, null);
    assert.equal(result.line, "");
  }
  assert.equal(sandbox.inspect.measure({ presentation_key: "progress.attach", presentation_args: { path: "D:/folder" } }).share, 0.7);
});
