import { Events, Window } from "/wails/runtime.js";
import * as HeadBrowser from "./bindings/filees/cmd/filees-gui-wails/headbrowserservice.js";
import { initializeTheme } from "./theme-preference.js";
import { initializeLanguage, t, getLocale } from "./i18n.js";
import { formatBytes, breadcrumb, joinPath, parentPath } from "./timemachine-view.js";
import { entryType, sortEntries, copyStatusKey } from "./headbrowser-view.js";

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
  const token = ++state.token;
  state.path = "";
  state.entries = [];
  await loadRepository();
  if (token !== state.token) return;
  if (state.repo) await list("");
}

async function list(path) {
  const token = ++state.token;
  state.loading = true;
  state.path = path;
  renderEntries();
  try {
    const result = await HeadBrowser.List(path);
    if (token !== state.token) return;
    state.path = result.path ?? path;
    state.entries = sortEntries(result.entries ?? []);
  } catch (error) {
    if (token !== state.token) return;
    state.entries = [];
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
  for (const button of ["#hb-fill", "#hb-open-copy"]) $(button).disabled = Boolean(state.busy);
}

function renderEntries() {
  const crumbs = breadcrumb(state.path).map(crumb => `<span aria-hidden="true">/</span><button type="button" class="tm-link" data-open-path="${escapeHTML(crumb.path)}">${escapeHTML(crumb.name)}</button>`);
  $("#hb-breadcrumb").innerHTML = `<button type="button" class="tm-link" data-open-path="">${escapeHTML(t("headBrowser.root"))}</button>${crumbs.join("")}`;
  $("#hb-progress").textContent = state.busy ? t(state.busy) : "";
  const listNode = $("#hb-entries");
  const up = state.path ? `<button type="button" class="tm-entry tm-up" data-open-path="${escapeHTML(parentPath(state.path))}">↑ ${escapeHTML(t("headBrowser.up"))}</button>` : "";
  if (!state.entries.length) {
    listNode.innerHTML = up + `<p class="tm-hint">${escapeHTML(t(state.loading ? "headBrowser.loading" : "headBrowser.emptyFolder"))}</p>`;
    return;
  }
  const disabled = state.busy ? " disabled" : "";
  listNode.innerHTML = up + state.entries.map(entry => {
    const path = joinPath(state.path, entry.name);
    const dir = entry.kind === "dir";
    const type = dir ? t("headBrowser.folder") : entryType(entry.name);
    const name = dir
      ? `<button type="button" class="hb-open" data-open-path="${escapeHTML(path)}">${escapeHTML(entry.name)}</button>`
      : `<button type="button" class="hb-open" data-preview="${escapeHTML(path)}" title="${escapeHTML(t("headBrowser.preview"))}">${escapeHTML(entry.name)}</button>`;
    const size = dir ? "" : escapeHTML(formatBytes(entry.size ?? 0, getLocale()));
    const local = entry.local ? `<span class="hb-local">${escapeHTML(t("headBrowser.local"))}</span>` : "<span></span>";
    const actions = [
      dir ? "" : `<button type="button" class="quiet-button" data-preview="${escapeHTML(path)}"${disabled}>${escapeHTML(t("headBrowser.preview"))}</button>`,
      entry.local
        ? `<button type="button" class="quiet-button" data-open-local="${escapeHTML(path)}"${disabled}>${escapeHTML(t("headBrowser.openLocal"))}</button>`
        : `<button type="button" class="primary-button" data-materialize="${escapeHTML(path)}"${disabled}>${escapeHTML(t("headBrowser.materialize"))}</button>`,
    ].join("");
    return `<div class="hb-row"><span class="hb-type${dir ? " is-dir" : ""}">${escapeHTML(type)}</span>${name}<small>${size}</small>${local}<span class="hb-actions">${actions}</span></div>`;
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

$("#hb-entries").addEventListener("click", event => {
  const target = event.target.closest("button");
  if (!target || target.disabled) return;
  if (target.dataset.openPath !== undefined) list(target.dataset.openPath);
  else if (target.dataset.preview !== undefined) withBusy("headBrowser.opening", () => HeadBrowser.Preview(target.dataset.preview));
  else if (target.dataset.openLocal !== undefined) HeadBrowser.OpenLocal(target.dataset.openLocal).catch(error => toast(errorText(error)));
  else if (target.dataset.materialize !== undefined) materialize(target.dataset.materialize);
});
$("#hb-breadcrumb").addEventListener("click", event => {
  const target = event.target.closest("[data-open-path]");
  if (target) list(target.dataset.openPath);
});
$("#hb-fill").addEventListener("click", () => fill());
$("#hb-open-copy").addEventListener("click", () => HeadBrowser.OpenLocal("").catch(error => toast(errorText(error))));
$("#hb-minimise").addEventListener("click", () => Window.Minimise());
$("#hb-close").addEventListener("click", () => HeadBrowser.Close());

Events.On("filees:head-browser-context", event => open(event?.data ?? event));
HeadBrowser.Context().then(context => {
  if (context?.repo_id) open(context);
  else renderHeader();
}).catch(() => renderHeader());
