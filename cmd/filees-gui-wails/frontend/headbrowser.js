import { Events, Window } from "/wails/runtime.js";
import * as HeadBrowser from "./bindings/filees/cmd/filees-gui-wails/headbrowserservice.js";
import { initializeTheme } from "./theme-preference.js";
import { initializeLanguage, t, getLocale } from "./i18n.js";
import { formatBytes, breadcrumb, joinPath, parentPath } from "./timemachine-view.js";
import { entryType, sortEntries, copyStatusKey, releaseOffered, releaseAllowed, materializeAllowed, availabilityKey, moveAllowed } from "./headbrowser-view.js";

// Przeglądaj na serwerze (implementation notes (not distributed)). The page
// asks the daemon for everything through HeadBrowserService: the folder at
// HEAD, a read-only preview, and bringing a path onto this computer. It
// decides nothing the daemon does not check again.

initializeTheme();
initializeLanguage();

const $ = selector => document.querySelector(selector);
const escapeHTML = value => String(value ?? "")
  .replaceAll("&", "&amp;")
  .replaceAll("<", "&lt;")
  .replaceAll(">", "&gt;")
  .replaceAll('"', "&quot;")
  .replaceAll("'", "&#039;");
const errorText = error => (typeof error === "string" ? error : String(error?.message ?? error ?? ""));

const OPERATION_POLL_MS = 1500;

// Folders get an icon, as in a file manager; files keep their type badge.
const FOLDER_ICON = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4.6l2 2h8.4A1.5 1.5 0 0 1 21 8.5v9A1.5 1.5 0 0 1 19.5 19h-15A1.5 1.5 0 0 1 3 17.5Z"/></svg>';

const state = { repo: null, path: "", entries: [], loading: false, busy: "", token: 0 };

function toast(message) {
  const node = document.createElement("article");
  node.className = "toast";
  const title = document.createElement("strong");
  title.textContent = t("headBrowser.errorTitle");
  const body = document.createElement("span");
  body.textContent = message;
  node.append(title, body);
  $("#hb-toasts").appendChild(node);
  window.setTimeout(() => node.remove(), 15000);
}

async function loadRepository() {
  try {
    state.repo = await HeadBrowser.Repository();
  } catch (error) {
    state.repo = null;
    toast(errorText(error));
  }
  renderHeader();
}

async function open(context) {
  $("#hb-move-dialog").close();
  moveDialogScope=null;
  const token = ++state.token;
  state.path = "";
  state.entries = [];
  await loadRepository();
  if (token !== state.token) return;
  if (state.repo) await list("");
}

async function list(path, quiet = false) {
  const token = ++state.token;
  state.loading = true;
  state.path = path;
  // The previous folder's rows must not survive into the render for this one:
  // left clickable while the request is in flight, a fast second click reads
  // a stale row's name against the new path and asks for a path that was
  // never a child of it (2026-09-26, reported from real use).
  if (!quiet) {
    state.entries = [];
    renderEntries();
  }
  try {
    const result = await HeadBrowser.List(path);
    if (token !== state.token) return;
    state.path = result.path ?? path;
    state.entries = sortEntries(result.entries ?? []);
  } catch (error) {
    if (token !== state.token) return;
    if (!quiet) state.entries = [];
    else state.entries = state.entries.map(entry => ({ ...entry, availability: "unknown" }));
    toast(errorText(error));
  } finally {
    if (token === state.token) {
      state.loading = false;
      renderEntries();
    }
  }
}

function renderHeader() {
  const repo = state.repo;
  document.body.classList.toggle("hb-is-empty", !repo);
  $("#hb-empty").hidden = Boolean(repo);
  $("#hb-card").hidden = !repo;
  $("#hb-server").textContent = repo?.server_name ?? "";
  $("#hb-name").textContent = repo?.name ?? "—";
  $("#hb-status").textContent = repo ? t(copyStatusKey(repo)) : "";
  $("#hb-fill").hidden = !(repo?.attached && repo?.sparse);
  $("#hb-open-copy").hidden = !repo?.attached;
  $("#hb-recover-move").hidden = !repo?.can_move;
  for (const button of ["#hb-fill", "#hb-open-copy", "#hb-recover-move"]) $(button).disabled = Boolean(state.busy);
}

function renderEntries() {
  const crumbs = breadcrumb(state.path).map(crumb => `<span aria-hidden="true">/</span><button type="button" class="tm-link" data-open-path="${escapeHTML(crumb.path)}">${escapeHTML(crumb.name)}</button>`);
  $("#hb-breadcrumb").innerHTML = `<button type="button" class="tm-link" data-open-path="">${escapeHTML(t("headBrowser.root"))}</button>${crumbs.join("")}`;
  $("#hb-progress").textContent = state.busy ? t(state.busy) : "";
  const listNode = $("#hb-entries");
  const up = state.path ? `<button type="button" class="tm-entry tm-up" data-open-path="${escapeHTML(parentPath(state.path))}">↑ ${escapeHTML(t("headBrowser.up"))}</button>` : "";
  if (!state.entries.length) {
    // A spinner, not just text: a network-bound list is exactly the interval
    // where an impatient click would otherwise land on a row from the folder
    // being left, so the loading state has to read as busy, not empty.
    const hint = state.loading
      ? `<p class="tm-hint hb-loading"><span class="hb-spinner" aria-hidden="true"></span>${escapeHTML(t("headBrowser.loading"))}</p>`
      : `<p class="tm-hint">${escapeHTML(t("headBrowser.emptyFolder"))}</p>`;
    listNode.innerHTML = up + hint;
    return;
  }
  const disabled = state.busy ? " disabled" : "";
  listNode.innerHTML = up + state.entries.map(entry => {
    const path = joinPath(state.path, entry.name);
    const dir = entry.kind === "dir";
    const availability = availabilityKey(entry);
    const contentState = availability ? availability.split(".").at(-1) : "unknown";
    const badge = dir
      ? `<span class="hb-folder is-${contentState}" title="${escapeHTML(t("headBrowser.folder"))}">${FOLDER_ICON}</span>`
      : `<span class="hb-type">${escapeHTML(entryType(entry.name))}</span>`;
    const name = dir
      ? `<button type="button" class="hb-open" data-open-path="${escapeHTML(path)}">${escapeHTML(entry.name)}</button>`
      : `<button type="button" class="hb-open" data-preview="${escapeHTML(path)}" title="${escapeHTML(t("headBrowser.previewHint"))}">${escapeHTML(entry.name)}</button>`;
    const size = dir ? "" : escapeHTML(formatBytes(entry.size ?? 0, getLocale()));
    const local = availability ? `<span class="hb-local is-${contentState}">${escapeHTML(t(availability))}</span>` : "<span></span>";
    const actions = [
      moveAllowed(state.repo,entry) ? `<button type="button" class="quiet-button" data-move="${escapeHTML(path)}"${disabled}>${escapeHTML(t("headBrowser.move"))}</button>` : "",
      dir ? "" : `<button type="button" class="quiet-button" data-preview="${escapeHTML(path)}" title="${escapeHTML(t("headBrowser.previewHint"))}"${disabled}>${escapeHTML(t("headBrowser.preview"))}</button>`,
      entry.local ? `<button type="button" class="quiet-button" data-open-local="${escapeHTML(path)}"${disabled}>${escapeHTML(t("headBrowser.openLocal"))}</button>` : "",
      materializeAllowed(state.repo, entry) ? `<button type="button" class="primary-button" data-materialize="${escapeHTML(path)}"${disabled}>${escapeHTML(t("headBrowser.materialize"))}</button>` : "",
      releaseOffered(state.repo, entry)
        ? `<button type="button" class="quiet-button" data-release="${escapeHTML(path)}" title="${escapeHTML(t("headBrowser.releaseHint"))}"${state.busy || !releaseAllowed(state.repo, entry) ? " disabled" : ""}>${escapeHTML(t("headBrowser.release"))}</button>`
        : "",
    ].join("");
    return `<div class="hb-row${dir ? " is-dir" : ""}">${badge}${name}<small>${size}</small>${local}<span class="hb-actions">${actions}</span></div>`;
  }).join("");
}

async function withBusy(key, action) {
  if (state.busy) return;
  state.busy = key;
  renderHeader();
  renderEntries();
  try {
    await action();
  } catch (error) {
    toast(errorText(error));
  } finally {
    state.busy = "";
    renderHeader();
    renderEntries();
  }
}

// The first chosen path starts the copy through the attach lifecycle; the
// page waits for it to become a working copy before listing again.
async function waitForAttachment(operationID) {
  for (;;) {
    const result = await HeadBrowser.Operation(operationID);
    if (result.state === "attached") return;
    if (result.state === "error" || result.state === "abandoned") {
      throw new Error(result.last_error || t("headBrowser.materializeFailed"));
    }
    await new Promise(resolve => window.setTimeout(resolve, OPERATION_POLL_MS));
  }
}

async function materialize(path) {
  await withBusy("headBrowser.bringing", async () => {
    const result = await HeadBrowser.Materialize(path);
    if (result.state === "cancelled") return;
    if (result.state === "attaching" && result.operation_id) await waitForAttachment(result.operation_id);
    await loadRepository();
  });
  await list(state.path);
}

async function fill() {
  await withBusy("headBrowser.filling", async () => {
    await HeadBrowser.Fill();
    await loadRepository();
  });
  await list(state.path);
}

async function release(path) {
  await withBusy("headBrowser.releasing", async () => {
    await HeadBrowser.Release(path);
    await loadRepository();
  });
  await list(state.path);
}

let moveDialogScope=null;
$("#hb-entries").addEventListener("click", event => {
  const target = event.target.closest("button");
  if (!target || target.disabled) return;
  if (target.dataset.openPath !== undefined) list(target.dataset.openPath);
  else if (target.dataset.preview !== undefined) withBusy("headBrowser.opening", () => HeadBrowser.Preview(target.dataset.preview));
  else if (target.dataset.openLocal !== undefined) HeadBrowser.OpenLocal(target.dataset.openLocal).catch(error => toast(errorText(error)));
  else if (target.dataset.materialize !== undefined) materialize(target.dataset.materialize);
  else if (target.dataset.release !== undefined) release(target.dataset.release);
  else if (target.dataset.move !== undefined) {
    moveDialogScope={token:state.token,server:state.repo.server_id,repo:state.repo.repo_id};
    $("#hb-move-source").textContent=target.dataset.move;
    $("#hb-move-target").value=target.dataset.move;
    $("#hb-move-dialog").showModal();
    $("#hb-move-target").focus();
  }
});
$("#hb-move-form").addEventListener("submit",async event=>{
  event.preventDefault();
  const source=$("#hb-move-source").textContent, destination=$("#hb-move-target").value;
  $("#hb-move-dialog").close();
  const scope=moveDialogScope;moveDialogScope=null;
  if (!scope || scope.token!==state.token || scope.server!==state.repo?.server_id || scope.repo!==state.repo?.repo_id) return;
  await withBusy("headBrowser.moving",()=>HeadBrowser.MoveFolder(source,destination));
  await list(state.path);
});
$("#hb-move-cancel").addEventListener("click",()=>$("#hb-move-dialog").close());
$("#hb-recover-move").addEventListener("click",async()=>{
  await withBusy("headBrowser.moving",()=>HeadBrowser.RecoverMove());
  await list(state.path);
});
$("#hb-breadcrumb").addEventListener("click", event => {
  const target = event.target.closest("[data-open-path]");
  if (target) list(target.dataset.openPath);
});
$("#hb-fill").addEventListener("click", () => fill());
$("#hb-open-copy").addEventListener("click", () => HeadBrowser.OpenLocal("").catch(error => toast(errorText(error))));
$("#hb-minimise").addEventListener("click", () => Window.Minimise());
$("#hb-close").addEventListener("click", () => HeadBrowser.Close());

// Explorer can hydrate/release while this window remains open. Refresh when
// returning to it, without a timer, overlapping calls or a flashing list.
window.addEventListener("focus", () => {
  if (state.repo && !state.busy && !state.loading && !$("#hb-move-dialog").open) return list(state.path, true);
});

Events.On("filees:head-browser-context", event => open(event?.data ?? event));
HeadBrowser.Context().then(context => {
  if (context?.repo_id) open(context);
  else renderHeader();
}).catch(() => renderHeader());
