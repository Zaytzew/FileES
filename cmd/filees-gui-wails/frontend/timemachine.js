import { Events, Window } from "/wails/runtime.js";
import * as TimeMachine from "./bindings/filees/cmd/filees-gui-wails/timemachineservice.js";
import { initializeTheme } from "./theme-preference.js";
import { initializeLanguage, t, formatNumber, getLocale } from "./i18n.js";
import {
  GRANULARITIES, formatBytes, utcOffsetLabel, localInputValue, parseBars, chartGeometry, intervalBounds,
  skipReasonKey, actionKey, isActive, isFinal, hasRoom, breadcrumb, joinPath, parentPath,
} from "./timemachine-view.js";

// Wehikuł czasu (concepts/REPOSITORY_HISTORY_CONCEPT.md §4, §7). The page asks
// the daemon for everything through TimeMachineService and decides nothing the
// daemon does not check again: which repository, which revision, what a copy
// contains and where it lands.

initializeTheme();
initializeLanguage();

const $ = selector => document.querySelector(selector);
const escapeHTML = value => String(value ?? "")
  .replaceAll("&", "&amp;")
  .replaceAll("<", "&lt;")
  .replaceAll(">", "&gt;")
  .replaceAll('"', "&quot;")
  .replaceAll("'", "&#039;");

const DENSITY_POLL_MS = 3000;
const OPERATION_POLL_MS = 800;
const repoKey = repo => JSON.stringify([repo.server_id, repo.repo_id]);

const state = {
  repos: [], repo: null, token: 0,
  snapshot: null, momentMs: NaN,
  granularity: 4, density: null, bars: [], selectedBar: null, densityTimer: 0,
  commits: [], commitsCursor: "", commitsLoading: false, expanded: new Map(),
  path: "", entries: [], entriesCursor: "", entriesLoading: false, selection: new Map(),
  operation: null, operationScope: null, operationTimer: 0,
};

const errorText = error => (typeof error === "string" ? error : String(error?.message ?? error ?? ""));
const formatter = options => new Intl.DateTimeFormat(getLocale(), options);
const dateTime = ms => formatter({ dateStyle: "medium", timeStyle: "medium" }).format(new Date(ms));
const shortDate = ms => formatter({ day: "numeric", month: "short" }).format(new Date(ms));
const shortDateTime = ms => formatter({ day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(new Date(ms));
const clock = ms => formatter({ hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(ms));
const offsetMinutes = ms => -new Date(Number.isFinite(ms) ? ms : Date.now()).getTimezoneOffset();
const bytes = value => formatBytes(value, getLocale());

function toast(message) {
  const node = document.createElement("article");
  node.className = "toast";
  const title = document.createElement("strong");
  title.textContent = t("timeMachine.errorTitle");
  const body = document.createElement("span");
  body.textContent = message;
  node.append(title, body);
  $("#tm-toasts").appendChild(node);
  window.setTimeout(() => node.remove(), 7000);
}

// ---- Loading ---------------------------------------------------------------

async function loadRepositories(focus) {
  let repos = [];
  try {
    repos = (await TimeMachine.Repositories()) ?? [];
  } catch (error) {
    toast(errorText(error));
  }
  state.repos = repos;
  const select = $("#tm-repository");
  select.replaceChildren(...repos.map(repo => {
    const option = document.createElement("option");
    option.value = repoKey(repo);
    option.textContent = [repo.server_name, repo.name].join(" / ");
    return option;
  }));
  const empty = repos.length === 0;
  $("#tm-empty").hidden = !empty;
  document.body.classList.toggle("tm-is-empty", empty);
  if (empty) {
    state.repo = null;
    state.token++;
    return;
  }
  const wanted = focus?.repo_id ? repos.find(repo => repo.server_id === focus.server_id && repo.repo_id === focus.repo_id) : null;
  const current = state.repo ? repos.find(repo => repoKey(repo) === repoKey(state.repo)) : null;
  const chosen = wanted ?? current ?? repos[0];
  select.value = repoKey(chosen);
  if (!current || wanted || repoKey(chosen) !== repoKey(current)) await openRepository(chosen);
}

async function openRepository(repo) {
  state.repo = repo;
  const token = ++state.token;
  window.clearTimeout(state.densityTimer);
  Object.assign(state, {
    snapshot: null, momentMs: NaN, density: null, bars: [], selectedBar: null,
    commits: [], commitsCursor: "", path: "", entries: [], entriesCursor: "",
  });
  state.expanded.clear();
  state.selection.clear();
  renderAll();
  await resolveMoment(new Date().toISOString(), "at", token);
  if (token === state.token) loadDensity(token);
}

// resolveMoment takes the moment as text: a save's own date keeps its
// microseconds, and rounding it to milliseconds would name the save before it.
async function resolveMoment(momentISO, boundary = "at", token = state.token) {
  if (!state.repo || !momentISO) return;
  let snapshot;
  try {
    snapshot = await TimeMachine.Resolve(state.repo.server_id, state.repo.repo_id, momentISO, boundary);
  } catch (error) {
    if (token === state.token) toast(errorText(error));
    return;
  }
  if (token !== state.token) return;
  const revisionChanged = state.snapshot?.revision !== snapshot.revision;
  state.snapshot = snapshot;
  state.momentMs = Date.parse(snapshot.requested_moment);
  if (revisionChanged) state.selection.clear();
  renderMoment();
  renderChart();
  renderSlider();
  renderCommits();
  renderSide();
  if (revisionChanged || !state.entries.length) await loadEntries(token, true);
}

async function loadDensity(token = state.token) {
  if (!state.snapshot) return;
  window.clearTimeout(state.densityTimer);
  const buckets = [];
  let cursor = "";
  let result = null;
  try {
    for (let page = 0; page < 20; page++) {
      result = await TimeMachine.Density(state.snapshot.snapshot_id, state.granularity, offsetMinutes(Date.now()), cursor);
      if (token !== state.token) return;
      buckets.push(...(result?.buckets ?? []));
      cursor = result?.next_cursor ?? "";
      if (!cursor) break;
    }
  } catch (error) {
    if (token === state.token) toast(errorText(error));
    return;
  }
  state.density = result;
  state.bars = parseBars(buckets);
  if (state.selectedBar) state.selectedBar = state.bars.find(bar => bar.startMs === state.selectedBar.startMs) ?? null;
  renderChart();
  renderSlider();
  if (result?.indexing && !document.hidden) {
    state.densityTimer = window.setTimeout(() => loadDensity(token), DENSITY_POLL_MS);
  }
}

async function loadCommits(reset = true, token = state.token) {
  const bar = state.selectedBar;
  if (!bar || !state.snapshot) {
    renderCommits();
    return;
  }
  if (reset) {
    state.commits = [];
    state.commitsCursor = "";
    state.expanded.clear();
  }
  state.commitsLoading = true;
  renderCommits();
  const { from, to } = intervalBounds(bar);
  try {
    const result = await TimeMachine.Commits(state.snapshot.snapshot_id, from, to, state.commitsCursor);
    if (token !== state.token || bar !== state.selectedBar) return;
    state.commits = state.commits.concat(result?.commits ?? []);
    state.commitsCursor = result?.next_cursor ?? "";
  } catch (error) {
    if (token === state.token) toast(errorText(error));
  }
  state.commitsLoading = false;
  renderCommits();
}

async function toggleChanges(revision) {
  if (state.expanded.has(revision)) {
    state.expanded.delete(revision);
    renderCommits();
    return;
  }
  state.expanded.set(revision, { changed: [], cursor: "", loading: true });
  renderCommits();
  await loadChanges(revision);
}

async function loadChanges(revision, token = state.token) {
  const entry = state.expanded.get(revision);
  if (!entry || !state.snapshot) return;
  entry.loading = true;
  try {
    const result = await TimeMachine.Changes(state.snapshot.snapshot_id, revision, entry.cursor);
    if (token !== state.token) return;
    entry.changed.push(...(result?.changed ?? []));
    entry.cursor = result?.next_cursor ?? "";
  } catch (error) {
    if (token === state.token) toast(errorText(error));
  }
  entry.loading = false;
  renderCommits();
}

async function loadEntries(token = state.token, reset = true) {
  if (!state.snapshot) return;
  if (reset) {
    state.entries = [];
    state.entriesCursor = "";
  }
  state.entriesLoading = true;
  renderEntries();
  try {
    const result = await TimeMachine.List(state.snapshot.snapshot_id, state.path, state.entriesCursor);
    if (token !== state.token) return;
    state.entries = state.entries.concat(result?.entries ?? []);
    state.entriesCursor = result?.next_cursor ?? "";
  } catch (error) {
    if (token !== state.token) return;
    state.entriesLoading = false;
    // The folder may not exist in this revision: the tree starts over at the root.
    if (state.path) {
      state.path = "";
      await loadEntries(token, true);
      return;
    }
    toast(errorText(error));
  }
  state.entriesLoading = false;
  renderEntries();
  renderSide();
}

// ---- Rendering -------------------------------------------------------------

function renderAll() {
  renderGranularity();
  renderChart();
  renderSlider();
  renderMoment();
  renderCommits();
  renderEntries();
  renderSide();
  if (state.operation) renderDialog();
}

function renderGranularity() {
  $("#tm-granularity").innerHTML = GRANULARITIES.map(hours => {
    const active = hours === state.granularity;
    return `<button type="button" role="radio" aria-checked="${active}" class="${active ? "active" : ""}" data-granularity="${hours}">${escapeHTML(t("timeMachine.hours", { hours }))}</button>`;
  }).join("");
}

function renderChart() {
  const root = $("#tm-chart");
  const geometry = chartGeometry(state.bars, state.momentMs);
  if (!geometry) {
    const key = !state.snapshot ? "timeMachine.loading" : !state.density || state.density.indexing ? "timeMachine.indexingStart" : "timeMachine.emptyHistory";
    root.innerHTML = `<p class="tm-chart-empty">${escapeHTML(t(key))}</p>`;
  } else {
    const bars = geometry.bars.map(({ bar, left, width, height }, index) => {
      const label = t("timeMachine.barLabel", { from: shortDateTime(bar.startMs), to: shortDateTime(bar.endMs), changes: formatNumber(bar.changed_paths) });
      const selected = state.selectedBar?.startMs === bar.startMs ? " selected" : "";
      return `<button type="button" class="tm-bar${selected}" data-bar="${index}" style="left:${left.toFixed(3)}%;width:${width.toFixed(3)}%;height:${height.toFixed(1)}%" aria-label="${escapeHTML(label)}" title="${escapeHTML(label)}"></button>`;
    }).join("");
    const marker = geometry.marker === null ? "" : `<span class="tm-marker" style="left:${geometry.marker.toFixed(3)}%" aria-hidden="true"></span>`;
    const ticks = Array.from({ length: 6 }, (_, index) => {
      const ms = geometry.start + (geometry.end - geometry.start) * index / 5;
      return `<span style="left:${index * 20}%">${escapeHTML(shortDate(ms))}</span>`;
    }).join("");
    root.innerHTML = `<div class="tm-bars">${bars}${marker}</div><div class="tm-axis">${ticks}</div>`;
  }
  const status = $("#tm-index-status");
  const density = state.density;
  status.textContent = !density ? ""
    : density.index_diagnostic ? t("timeMachine.indexProblem")
      : density.indexing ? t("timeMachine.indexing", { indexed: density.indexed_revision, head: density.head_revision })
        : "";
}

function renderSlider() {
  const input = $("#tm-moment");
  const first = state.bars[0]?.startMs ?? (state.density?.first_date ? Date.parse(state.density.first_date) : NaN);
  const now = Date.now();
  const usable = Boolean(state.snapshot) && Number.isFinite(first) && first < now;
  input.disabled = !usable;
  if (usable) {
    input.min = String(first);
    input.max = String(now);
    if (document.activeElement !== input) input.value = String(Number.isFinite(state.momentMs) ? state.momentMs : now);
  }
  $("#tm-first-date").textContent = usable ? dateTime(first) : "—";
  $("#tm-head-date").textContent = state.snapshot ? dateTime(now) : "—";
  const head = Math.max(Number(state.density?.head_revision ?? 0), Number(state.snapshot?.revision ?? 0));
  $("#tm-head-rev").textContent = head ? t("timeMachine.head", { revision: head }) : "";
  $("#tm-range").textContent = usable ? `${shortDate(first)} – ${shortDate(now)}` : "";
}

function renderMoment() {
  const known = Number.isFinite(state.momentMs);
  $("#tm-moment-label").textContent = known ? dateTime(state.momentMs) : "—";
  $("#tm-snapshot-badge").textContent = state.snapshot ? t("timeMachine.stateBadge", { revision: state.snapshot.revision }) : "";
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  $("#tm-zone").textContent = t("timeMachine.zone", { zone, offset: utcOffsetLabel(offsetMinutes(state.momentMs)) });
  const exact = $("#tm-exact");
  if (known && document.activeElement !== exact) exact.value = localInputValue(state.momentMs);
}

function renderCommits() {
  const head = $("#tm-interval-head");
  const list = $("#tm-commits");
  const bar = state.selectedBar;
  $("#tm-changes-count").textContent = bar ? formatNumber(bar.changed_paths) : "";
  if (!bar) {
    head.innerHTML = "";
    list.innerHTML = `<p class="tm-hint">${escapeHTML(t("timeMachine.pickBar"))}</p>`;
    return;
  }
  const unique = bar.unique_exact ? formatNumber(bar.unique_paths) : t("timeMachine.uniqueAtLeast", { count: formatNumber(bar.unique_paths) });
  head.innerHTML = `<div><strong>${escapeHTML(t("timeMachine.interval", { from: shortDateTime(bar.startMs), to: shortDateTime(bar.endMs) }))}</strong>`
    + `<small>${escapeHTML(t("timeMachine.intervalSummary", { changes: formatNumber(bar.changed_paths), unique, commits: formatNumber(bar.commits) }))}</small></div>`
    + `<button type="button" class="quiet-button" data-state-at-end>${escapeHTML(t("timeMachine.stateAtEnd"))}</button>`;
  if (!state.commits.length) {
    list.innerHTML = `<p class="tm-hint">${escapeHTML(t(state.commitsLoading ? "timeMachine.loading" : "timeMachine.noChanges"))}</p>`;
    return;
  }
  const rows = state.commits.map(commit => {
    const ms = Date.parse(commit.date);
    const selected = state.snapshot?.revision === commit.revision ? " selected" : "";
    const expanded = state.expanded.get(commit.revision);
    let changes = "";
    if (expanded) {
      const items = expanded.changed.map(change => `<li><span class="tm-action tm-action-${escapeHTML(change.action)}">${escapeHTML(t(actionKey(change.action)))}</span>`
        + `<span class="tm-path">${escapeHTML(change.path || t("timeMachine.root"))}</span>`
        + (change.copyfrom_path ? `<small>${escapeHTML(t("timeMachine.copiedFrom", { path: change.copyfrom_path, revision: change.copyfrom_rev }))}</small>` : "")
        + `</li>`).join("");
      changes = `<ul class="tm-changes">${items}</ul>`
        + (expanded.loading ? `<p class="tm-hint">${escapeHTML(t("timeMachine.loading"))}</p>` : "")
        + (expanded.cursor && !expanded.loading ? `<button type="button" class="tm-link" data-more-changes="${escapeHTML(commit.revision)}">${escapeHTML(t("timeMachine.more"))}</button>` : "");
    }
    const shout = commit.shout ? `<span class="tm-shout"><small>${escapeHTML(t("timeMachine.shout"))}</small>${escapeHTML(commit.shout)}</span>` : "";
    return `<article class="tm-commit${selected}"><div class="tm-commit-main">`
      + `<button type="button" class="tm-time" data-pick-commit="${escapeHTML(commit.date)}" title="${escapeHTML(t("timeMachine.pickCommit"))}">${escapeHTML(Number.isFinite(ms) ? clock(ms) : commit.date)}</button>`
      + `<span class="tm-badge">r${escapeHTML(commit.revision)}</span>${shout}`
      + `<button type="button" class="tm-link tm-push" data-toggle-changes="${escapeHTML(commit.revision)}" aria-expanded="${expanded ? "true" : "false"}">${escapeHTML(t("timeMachine.changedCount", { count: formatNumber(commit.changed_count) }))}</button>`
      + `</div>${changes}</article>`;
  }).join("");
  const more = state.commitsCursor && !state.commitsLoading ? `<button type="button" class="quiet-button tm-more" data-more-commits>${escapeHTML(t("timeMachine.more"))}</button>` : "";
  list.innerHTML = rows + more;
}

function renderEntries() {
  const crumbs = breadcrumb(state.path).map(crumb => `<span aria-hidden="true">/</span><button type="button" class="tm-link" data-open-path="${escapeHTML(crumb.path)}">${escapeHTML(crumb.name)}</button>`);
  $("#tm-breadcrumb").innerHTML = `<button type="button" class="tm-link" data-open-path="">${escapeHTML(t("timeMachine.root"))}</button>${crumbs.join("")}`;
  $("#tm-files-count").textContent = state.entries.length ? formatNumber(state.entries.length) + (state.entriesCursor ? "+" : "") : "";
  const list = $("#tm-entries");
  if (!state.snapshot) {
    list.innerHTML = "";
    return;
  }
  const up = state.path ? `<button type="button" class="tm-entry tm-up" data-open-path="${escapeHTML(parentPath(state.path))}">↑ ${escapeHTML(t("timeMachine.up"))}</button>` : "";
  if (!state.entries.length) {
    list.innerHTML = up + `<p class="tm-hint">${escapeHTML(t(state.entriesLoading ? "timeMachine.loading" : "timeMachine.emptyFolder"))}</p>`;
    return;
  }
  const rows = state.entries.map(entry => {
    const path = joinPath(state.path, entry.name);
    const changed = Date.parse(entry.last_changed_date ?? "");
    const meta = t("timeMachine.lastChange", { revision: entry.last_changed_revision, date: Number.isFinite(changed) ? shortDateTime(changed) : "—" });
    if (entry.kind === "dir") {
      return `<button type="button" class="tm-entry tm-dir" data-open-path="${escapeHTML(path)}"><span class="tm-icon" aria-hidden="true">▸</span>`
        + `<span class="tm-name">${escapeHTML(entry.name)}</span><small>${escapeHTML(t("timeMachine.folder"))}</small><small>${escapeHTML(meta)}</small></button>`;
    }
    const checked = state.selection.has(path) ? " checked" : "";
    return `<label class="tm-entry tm-file"><input type="checkbox" data-select-path="${escapeHTML(path)}" data-size="${escapeHTML(entry.size ?? 0)}"${checked}>`
      + `<span class="tm-name">${escapeHTML(entry.name)}</span><small>${escapeHTML(bytes(entry.size ?? 0))}</small><small>${escapeHTML(meta)}</small></label>`;
  }).join("");
  const more = state.entriesCursor && !state.entriesLoading ? `<button type="button" class="quiet-button tm-more" data-more-entries>${escapeHTML(t("timeMachine.more"))}</button>` : "";
  list.innerHTML = up + rows + more;
}

function renderSide() {
  const snapshot = state.snapshot;
  $("#tm-side-moment").textContent = Number.isFinite(state.momentMs) ? dateTime(state.momentMs) : "—";
  $("#tm-side-revision").textContent = snapshot ? t("timeMachine.stateRevision", { revision: snapshot.revision }) : "";
  const saved = snapshot?.saved_at ? Date.parse(snapshot.saved_at) : NaN;
  $("#tm-side-saved").textContent = !snapshot ? ""
    : snapshot.revision > 0 && Number.isFinite(saved) ? t("timeMachine.savedAt", { date: dateTime(saved) }) : t("timeMachine.beforeFirst");
  const count = state.selection.size;
  $("#tm-side-selection").textContent = count ? t("timeMachine.selection", { count: formatNumber(count) }) : "";
  const usable = Boolean(snapshot && snapshot.revision > 0) && !isActive(state.operation?.state);
  const selected = $("#tm-download-selected");
  selected.disabled = !usable || count === 0;
  selected.textContent = t(count === 1 ? "timeMachine.downloadFile" : "timeMachine.downloadSelected");
  $("#tm-download-folder").disabled = !usable || !state.path;
  $("#tm-download-state").disabled = !usable;
}

// ---- Export ----------------------------------------------------------------

function scopeText(scope) {
  if (scope?.kind === "subtree") return t("timeMachine.scopeFolder", { path: scope.path });
  if (scope?.kind === "paths") return t("timeMachine.scopeFiles", { count: formatNumber(scope.paths.length) });
  return t("timeMachine.scopeAll");
}

const fact = (key, value) => `<div class="tm-fact"><small>${escapeHTML(t(key))}</small><strong>${escapeHTML(value)}</strong></div>`;

function skippedList(skipped) {
  if (!skipped?.length) return "";
  const items = skipped.map(skip => `<li><span class="tm-path">${escapeHTML(skip.repo_path)}</span><small>${escapeHTML(t(skipReasonKey(skip.reason)))}</small></li>`).join("");
  return `<details class="tm-skipped"><summary>${escapeHTML(t("timeMachine.skipped", { count: formatNumber(skipped.length) }))}</summary><ul>${items}</ul></details>`;
}

async function startExport(scope) {
  const snapshot = state.snapshot;
  if (!snapshot || snapshot.revision < 1 || isActive(state.operation?.state)) return;
  let parent = "";
  try {
    parent = await TimeMachine.ChooseDestination();
  } catch (error) {
    toast(errorText(error));
    return;
  }
  if (!parent) return;
  state.operationScope = scope;
  state.operation = { state: "planning", destination_parent: parent, revision: snapshot.revision };
  $("#tm-dialog").hidden = false;
  renderDialog();
  renderSide();
  $("#tm-dialog-cancel").focus();
  try {
    const operation = await TimeMachine.Fetch({
      snapshot_id: snapshot.snapshot_id, selection: scope.kind, path: scope.path ?? "", paths: scope.paths ?? [],
      destination_parent: parent, utc_offset_minutes: offsetMinutes(state.momentMs),
    });
    trackOperation(operation);
  } catch (error) {
    closeDialog();
    toast(errorText(error));
  }
}

function trackOperation(operation) {
  window.clearTimeout(state.operationTimer);
  state.operation = operation;
  renderDialog();
  renderSide();
  if (["planning", "fetching", "finalizing"].includes(operation?.state)) {
    state.operationTimer = window.setTimeout(pollOperation, OPERATION_POLL_MS);
  }
}

async function pollOperation() {
  const id = state.operation?.operation_id;
  if (!id) return;
  try {
    trackOperation(await TimeMachine.Operation(id));
  } catch (error) {
    toast(errorText(error));
    state.operationTimer = window.setTimeout(pollOperation, OPERATION_POLL_MS * 4);
  }
}

function renderDialog() {
  const operation = state.operation;
  if (!operation) return;
  const title = $("#tm-dialog-title");
  const body = $("#tm-dialog-body");
  const cancel = $("#tm-dialog-cancel");
  const confirm = $("#tm-dialog-confirm");
  const phase = operation.state;
  const facts = [
    fact("timeMachine.fieldRepository", state.repo ? `${state.repo.server_name} / ${state.repo.name}` : ""),
    fact("timeMachine.fieldRevision", `r${operation.revision ?? state.snapshot?.revision ?? ""}`),
    fact("timeMachine.fieldMoment", Number.isFinite(state.momentMs) ? dateTime(state.momentMs) : "—"),
    fact("timeMachine.fieldScope", scopeText(state.operationScope)),
    fact("timeMachine.fieldDestination", operation.destination_parent ?? ""),
  ];
  confirm.hidden = true;
  cancel.disabled = false;
  cancel.textContent = t("timeMachine.cancel");

  if (phase === "planning") {
    title.textContent = t("timeMachine.dialogPlanning");
    body.innerHTML = `<p>${escapeHTML(t("timeMachine.planningText"))}</p><div class="tm-facts">${facts.join("")}</div>`;
    return;
  }
  if (phase === "planned") {
    const room = hasRoom(operation);
    title.textContent = t("timeMachine.dialogConfirm");
    facts.push(
      fact("timeMachine.fieldFiles", formatNumber(operation.files_total ?? 0)),
      fact("timeMachine.fieldSize", bytes(operation.bytes_total ?? 0)),
      fact("timeMachine.fieldFree", bytes(operation.space_available ?? 0)),
    );
    const renamed = operation.renamed?.length ? `<p>${escapeHTML(t("timeMachine.renamed", { count: formatNumber(operation.renamed.length) }))}</p>` : "";
    const whale = operation.whale_excluded ? `<p class="tm-warning">${escapeHTML(t("timeMachine.whale", { files: formatNumber(operation.whale_files ?? 0), size: bytes(operation.whale_bytes ?? 0) }))}</p>` : "";
    const space = room ? "" : `<p class="tm-warning">${escapeHTML(t("timeMachine.notEnoughSpace"))}</p>`;
    body.innerHTML = `<div class="tm-facts">${facts.join("")}</div>`
      + `<p>${escapeHTML(t("timeMachine.newSubfolder"))} <strong>${escapeHTML(t("timeMachine.noWorkingCopyChange"))}</strong></p>`
      + renamed + skippedList(operation.skipped) + whale + space;
    confirm.hidden = false;
    confirm.disabled = !room;
    confirm.textContent = t("timeMachine.confirm");
    return;
  }
  if (phase === "fetching" || phase === "finalizing") {
    const total = Number(operation.files_total ?? 0);
    const done = Number(operation.files_done ?? 0);
    const percent = total ? Math.round(done / total * 100) : 0;
    title.textContent = t("timeMachine.dialogProgress");
    const line = phase === "finalizing" ? t("timeMachine.finalizing")
      : t("timeMachine.progress", { files: formatNumber(done), total: formatNumber(total), bytes: bytes(operation.bytes_done ?? 0) });
    body.innerHTML = `<div class="tm-facts">${facts.join("")}</div><progress max="100" value="${percent}"></progress><p>${escapeHTML(line)}</p>`;
    // Once the copy is being moved under its final name it can no longer be undone.
    cancel.disabled = phase === "finalizing";
    return;
  }

  cancel.textContent = t("timeMachine.close");
  const lines = [];
  if (phase === "complete") {
    title.textContent = t("timeMachine.complete");
    lines.push(t("timeMachine.completePath", { path: operation.final_path ?? "" }));
  } else if (phase === "cancelled") {
    title.textContent = t("timeMachine.cancelled");
    lines.push(t("timeMachine.cancelledText"));
  } else if (phase === "interrupted") {
    title.textContent = t("timeMachine.interrupted");
    lines.push(t("timeMachine.interruptedText"));
  } else {
    title.textContent = t("timeMachine.failed");
    if (operation.final_path) lines.push(t("timeMachine.leftovers", { path: operation.final_path }));
    if (operation.staging_path) lines.push(t("timeMachine.leftovers", { path: operation.staging_path }));
  }
  if (operation.cleanup_error && operation.staging_path) lines.push(t("timeMachine.cleanupFailed", { path: operation.staging_path }));
  body.innerHTML = lines.map(text => `<p>${escapeHTML(text)}</p>`).join("")
    + (phase === "complete" ? skippedList(operation.skipped) : "");
}

async function dialogCancel() {
  const operation = state.operation;
  if (!operation || isFinal(operation.state) || !operation.operation_id) {
    if (!operation || isFinal(operation.state)) closeDialog();
    return;
  }
  if (operation.state === "finalizing") return;
  window.clearTimeout(state.operationTimer);
  try {
    trackOperation(await TimeMachine.Cancel(operation.operation_id));
  } catch (error) {
    toast(errorText(error));
    pollOperation();
  }
}

async function dialogConfirm() {
  const operation = state.operation;
  if (operation?.state !== "planned") return;
  $("#tm-dialog-confirm").disabled = true;
  try {
    trackOperation(await TimeMachine.Confirm(operation.operation_id));
  } catch (error) {
    toast(errorText(error));
    pollOperation();
  }
}

function closeDialog() {
  window.clearTimeout(state.operationTimer);
  state.operation = null;
  state.operationScope = null;
  $("#tm-dialog").hidden = true;
  renderSide();
}

// ---- Wiring ----------------------------------------------------------------

function showTab(tab) {
  for (const [name, button, panel] of [["changes", "#tab-changes", "#panel-changes"], ["files", "#tab-files", "#panel-files"]]) {
    const active = name === tab;
    $(button).classList.toggle("active", active);
    $(button).setAttribute("aria-selected", String(active));
    $(panel).hidden = !active;
  }
}

function openPath(path) {
  state.path = path;
  loadEntries(state.token, true);
  renderSide();
}

$("#tm-repository").addEventListener("change", event => {
  const repo = state.repos.find(item => repoKey(item) === event.target.value);
  if (repo) openRepository(repo);
});
$("#tm-granularity").addEventListener("click", event => {
  const button = event.target.closest("[data-granularity]");
  if (!button) return;
  state.granularity = Number(button.dataset.granularity);
  state.selectedBar = null;
  state.commits = [];
  renderGranularity();
  renderCommits();
  loadDensity();
});
$("#tm-chart").addEventListener("click", event => {
  const button = event.target.closest("[data-bar]");
  if (!button) return;
  // A bar chooses the interval of changes; the moment of the state stays put.
  state.selectedBar = state.bars[Number(button.dataset.bar)] ?? null;
  showTab("changes");
  renderChart();
  loadCommits(true);
});
const slider = $("#tm-moment");
slider.addEventListener("input", () => {
  $("#tm-moment-label").textContent = dateTime(Number(slider.value));
});
slider.addEventListener("change", () => resolveMoment(new Date(Number(slider.value)).toISOString()));
$("#tm-goto").addEventListener("submit", event => {
  event.preventDefault();
  const ms = new Date($("#tm-exact").value).getTime();
  if (Number.isFinite(ms)) resolveMoment(new Date(ms).toISOString());
});
$("#tm-interval-head").addEventListener("click", event => {
  // The state at the end of [from, to) is the newest save strictly before "to".
  if (event.target.closest("[data-state-at-end]") && state.selectedBar) {
    resolveMoment(new Date(state.selectedBar.endMs).toISOString(), "before");
  }
});
$("#tm-commits").addEventListener("click", event => {
  const pick = event.target.closest("[data-pick-commit]");
  if (pick) return void resolveMoment(pick.dataset.pickCommit);
  const toggle = event.target.closest("[data-toggle-changes]");
  if (toggle) return void toggleChanges(Number(toggle.dataset.toggleChanges));
  const more = event.target.closest("[data-more-changes]");
  if (more) return void loadChanges(Number(more.dataset.moreChanges));
  if (event.target.closest("[data-more-commits]")) loadCommits(false);
});
for (const selector of ["#tm-entries", "#tm-breadcrumb"]) {
  $(selector).addEventListener("click", event => {
    if (event.target.closest("input, label.tm-file")) return;
    const open = event.target.closest("[data-open-path]");
    if (open) return void openPath(open.dataset.openPath);
    if (event.target.closest("[data-more-entries]")) loadEntries(state.token, false);
  });
}
$("#tm-entries").addEventListener("change", event => {
  const box = event.target.closest("[data-select-path]");
  if (!box) return;
  if (box.checked) state.selection.set(box.dataset.selectPath, Number(box.dataset.size));
  else state.selection.delete(box.dataset.selectPath);
  renderSide();
});
$("#tm-download-selected").addEventListener("click", () => {
  if (state.selection.size) startExport({ kind: "paths", paths: [...state.selection.keys()] });
});
$("#tm-download-folder").addEventListener("click", () => {
  if (state.path) startExport({ kind: "subtree", path: state.path });
});
$("#tm-download-state").addEventListener("click", () => startExport({ kind: "all" }));
$("#tm-dialog-cancel").addEventListener("click", dialogCancel);
$("#tm-dialog-confirm").addEventListener("click", dialogConfirm);
$("#tab-changes").addEventListener("click", () => showTab("changes"));
$("#tab-files").addEventListener("click", () => showTab("files"));
$("#tm-minimise").addEventListener("click", () => Window.Minimise());
$("#tm-close").addEventListener("click", () => TimeMachine.Close());
$("#tm-titlebar").addEventListener("dblclick", event => {
  if (!event.target.closest(".window-controls")) Window.ToggleMaximise();
});
document.addEventListener("keydown", event => {
  if (event.key === "Escape" && state.operation && isFinal(state.operation.state)) closeDialog();
});
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && state.density?.indexing) loadDensity();
});
window.addEventListener("filees:language-changed", renderAll);
Events.On("filees:timemachine-context", event => loadRepositories(event?.data ?? event));

renderAll();
TimeMachine.Context().then(focus => loadRepositories(focus), () => loadRepositories(null));
