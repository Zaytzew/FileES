import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { languages } from "../frontend/i18n.js";
import {
  GRANULARITIES, SPACE_MARGIN, formatBytes, utcOffsetLabel, localInputValue, parseBars, chartGeometry,
  intervalBounds, skipReasonKey, actionKey, isActive, isFinal, hasRoom, breadcrumb, joinPath, parentPath,
} from "../frontend/timemachine-view.js";

const catalogues = Object.fromEntries(languages.map(language => [language.code, language.messages]));
const read = name => readFileSync(new URL(`../frontend/${name}`, import.meta.url), "utf8");

test("time machine page and script keys resolve in every catalogue", () => {
  const html = read("timemachine.html");
  const script = read("timemachine.js") + read("timemachine-view.js");
  const keys = new Set([...html.matchAll(/data-i18n(?:-[\w-]+)?="([^"]+)"/g)].map(match => match[1]));
  for (const [, key] of script.matchAll(/["']((?:timeMachine|window)\.[\w.]+)["']/g)) keys.add(key);
  for (const reason of ["reserved_device", "reserved_rune", "control_rune", "trailing_dot_or_space", "working_copy_name", "special", "unknown"]) {
    keys.add(skipReasonKey(reason === "unknown" ? "anything-else" : reason));
  }
  for (const action of ["A", "M", "D", "R"]) keys.add(actionKey(action));
  // Rendered by the Go service, not the page.
  keys.add("timeMachine.chooseDestination");
  keys.add("timeMachine.pickerUnavailable");
  assert.ok(keys.size > 80, `only ${keys.size} keys found`);
  for (const { code } of languages) {
    for (const key of keys) assert.equal(typeof catalogues[code][key], "string", `${code}:${key}`);
  }
  // The owner's wording for the full export, and never the word "snapshot".
  assert.equal(catalogues.pl["timeMachine.downloadState"], "Pobierz zapis tego stanu…");
  for (const { code } of languages) {
    for (const key of keys) assert.doesNotMatch(catalogues[code][key], /snapshot/i, `${code}:${key}`);
  }
});

test("the page reaches the daemon only through the time machine binding", () => {
  const script = read("timemachine.js");
  assert.match(script, /from "\.\/bindings\/filees\/cmd\/filees-gui-wails\/timemachineservice\.js"/);
  assert.match(script, /Events\.On\("filees:timemachine-context"/);
  assert.doesNotMatch(script, /fetch\(|XMLHttpRequest|window\.open\(/);
  // Every server-provided value that reaches innerHTML goes through escapeHTML.
  assert.doesNotMatch(script, /\$\{(?:repo|commit|change|entry|skip|operation)\.[a-z_]+\}/);
});

test("chart geometry keeps every bar and places the moment on the same axis", () => {
  const hour = 3600 * 1000;
  const base = Date.parse("2026-09-12T00:00:00Z");
  const bars = parseBars([
    { start: new Date(base + 8 * hour).toISOString(), end: new Date(base + 12 * hour).toISOString(), changed_paths: 8 },
    { start: new Date(base).toISOString(), end: new Date(base + 4 * hour).toISOString(), changed_paths: 2 },
    { start: "not a date", end: "2026-09-12T01:00:00Z", changed_paths: 99 },
  ]);
  assert.equal(bars.length, 2);
  assert.ok(bars[0].startMs < bars[1].startMs);
  const geometry = chartGeometry(bars, base + 10 * hour);
  assert.equal(geometry.bars.length, 2);
  assert.equal(geometry.bars[0].left, 0);
  assert.ok(Math.abs(geometry.bars[1].left - 200 / 3) < 1e-9);
  assert.equal(geometry.bars[1].height, 100);
  assert.equal(geometry.bars[0].height, 25);
  assert.ok(Math.abs(geometry.marker - 250 / 3) < 1e-9);
  const later = chartGeometry(bars, base + 14 * hour);
  assert.equal(later.end, base + 14 * hour);
  assert.equal(later.marker, 100);
  assert.equal(chartGeometry([], base), null);
  const tiny = chartGeometry(parseBars([{ start: new Date(base).toISOString(), end: new Date(base + hour).toISOString(), changed_paths: 0 }]), NaN);
  assert.ok(tiny.bars[0].height >= 4 && tiny.marker === null);
});

test("a bar's interval reaches the daemon as an inclusive range ending just before the next bar", () => {
  const bounds = intervalBounds({ startMs: Date.parse("2026-09-12T08:00:00Z"), endMs: Date.parse("2026-09-12T12:00:00Z") });
  assert.deepEqual(bounds, { from: "2026-09-12T08:00:00.000Z", to: "2026-09-12T11:59:59.999Z" });
});

test("export helpers mirror the daemon's rules without deciding for it", () => {
  assert.equal(hasRoom({ bytes_total: 1, space_available: SPACE_MARGIN }), false);
  assert.equal(hasRoom({ bytes_total: 1, space_available: SPACE_MARGIN + 1 }), true);
  assert.equal(skipReasonKey("special"), "timeMachine.reason.special");
  assert.equal(skipReasonKey("<script>"), "timeMachine.reason.unknown");
  assert.equal(actionKey("D"), "timeMachine.action.D");
  assert.equal(actionKey("X"), "timeMachine.action.M");
  for (const state of ["planning", "planned", "fetching", "finalizing"]) assert.ok(isActive(state) && !isFinal(state), state);
  for (const state of ["complete", "failed", "cancelled", "interrupted"]) assert.ok(isFinal(state) && !isActive(state), state);
  assert.deepEqual(GRANULARITIES, [1, 2, 4, 6, 12, 24]);
});

test("paths, zones and sizes render without losing what the repository says", () => {
  assert.deepEqual(breadcrumb("01_EDITABLES/2026 wiosna/rzuty"), [
    { name: "01_EDITABLES", path: "01_EDITABLES" },
    { name: "2026 wiosna", path: "01_EDITABLES/2026 wiosna" },
    { name: "rzuty", path: "01_EDITABLES/2026 wiosna/rzuty" },
  ]);
  assert.deepEqual(breadcrumb(""), []);
  assert.equal(joinPath("", "a.txt"), "a.txt");
  assert.equal(joinPath("Docs", "a.txt"), "Docs/a.txt");
  assert.equal(parentPath("Docs/deep/x"), "Docs/deep");
  assert.equal(parentPath("Docs"), "");
  assert.equal(utcOffsetLabel(120), "+02:00");
  assert.equal(utcOffsetLabel(-330), "−05:30");
  const moment = Date.parse("2026-09-12T10:04:05Z");
  assert.equal(new Date(localInputValue(moment)).getTime(), moment);
  assert.match(formatBytes(1536, "en"), /1\.5/);
  assert.match(formatBytes(-4, "pl"), /^0/);
  assert.match(formatBytes(3 * 1024 ** 3, "de"), /3/);
});
