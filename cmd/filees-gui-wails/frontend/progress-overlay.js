// Long waits the daemon cannot shorten - creating a repository on the server,
// its first publication, the first checkout of an attached one - cover the
// whole main window (progress_service.go). An elegant queue label was not
// enough: on Fedora, 2026-09-24, a dozen silent seconds after "Create" read as
// a hang. "Work in the background" (offered after a short while) parks the
// wait as a strip under the hero; the strip brings it back on top.
import { Events } from "/wails/runtime.js";
import { t } from "./i18n.js";

const BACKGROUND_AFTER_MS = 10000;

let items = [];
// "Work in the background" holds for the whole operation, not one stage: the
// import stage must not pop back over the window the user just freed.
let parked = false;
let overlay = null;
let strip = null;
let ticker = null;
// The wait the user sees is the whole operation: the clock and the offer to
// work in the background count from its first stage, not the current one.
let operationStart = null;
let context = { snapshot: () => null, bytes: (value) => String(value) };

// Localized stage text, or the Polish text the controller sent when the
// catalogue has no entry for the key.
function stageText(item, part) {
  const fallback = part === "title" ? item.title : item.text;
  if (!item.presentation_key) return fallback || "";
  const text = t(`${item.presentation_key}.${part}`, item.presentation_args || {});
  return text.startsWith("[") ? (fallback || "") : text;
}

function elapsed() {
  const started = operationStart;
  const seconds = Number.isFinite(started) ? Math.max(0, Math.floor((Date.now() - started) / 1000)) : 0;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
}

function samePath(a, b) {
  const norm = (value) => String(value || "").replaceAll("\\", "/").replace(/\/+$/, "").toLowerCase();
  return norm(a) !== "" && norm(a) === norm(b);
}

function repoFor(item) {
  const path = item.presentation_args?.path;
  if (!path) return null;
  return (context.snapshot()?.repositories || []).find((candidate) => samePath(candidate.local_path, path)) || null;
}

// The publication's own report (filees-svn commit --progress, through the
// daemon's publish_progress) is the real measure; the queue is the fallback
// for a helper that cannot report, and for the moments before sending starts.
function measure(item) {
  const repo = repoFor(item);
  if (!repo) return { line: "", share: null };
  const total = Number(repo.publish_files_total) || 0;
  if (total > 0) {
    const done = Number(repo.publish_files_done) || 0;
    const bytesTotal = Number(repo.publish_bytes_total) || 0;
    const bytesSent = Number(repo.publish_bytes_sent) || 0;
    const share = bytesTotal > 0 ? bytesSent / bytesTotal : done / total;
    return {
      line: t("progress.published", { done, total, sent: context.bytes(bytesSent), size: context.bytes(bytesTotal) }),
      share: Math.max(0, Math.min(1, share)),
    };
  }
  if (Number(repo.pending_files) > 0) {
    return { line: t("progress.queue", { files: repo.pending_files, size: context.bytes(repo.pending_bytes) }), share: null };
  }
  return { line: "", share: null };
}

function ensureOverlay() {
  if (overlay) return overlay;
  overlay = document.createElement("div");
  overlay.className = "progress-overlay";
  overlay.setAttribute("role", "alertdialog");
  overlay.setAttribute("aria-modal", "true");
  overlay.setAttribute("aria-live", "polite");
  overlay.innerHTML = `<div class="progress-card">
    <div class="progress-spinner" aria-hidden="true"></div>
    <h2 class="progress-title"></h2>
    <p class="progress-text"></p>
    <p class="progress-queue"></p>
    <div class="progress-bar" hidden><span></span></div>
    <p class="progress-elapsed"></p>
    <button type="button" class="progress-background" hidden></button>
  </div>`;
  overlay.querySelector(".progress-background").addEventListener("click", () => {
    parked = true;
    render();
  });
  document.body.appendChild(overlay);
  return overlay;
}

function ensureStrip() {
  if (strip) return strip;
  strip = document.createElement("button");
  strip.type = "button";
  strip.className = "progress-parking";
  strip.innerHTML = `<span class="progress-spinner is-small" aria-hidden="true"></span><span class="progress-parking-text"></span><span class="progress-parking-show"></span>`;
  strip.addEventListener("click", () => { parked = false; render(); });
  const hero = document.getElementById("top");
  if (hero?.parentNode) hero.parentNode.insertBefore(strip, hero.nextSibling);
  else document.body.prepend(strip);
  return strip;
}

function render() {
  const current = items[items.length - 1];
  if (!current) {
    overlay?.remove(); overlay = null;
    strip?.remove(); strip = null;
    parked = false;
    operationStart = null;
    if (ticker) { clearInterval(ticker); ticker = null; }
    return;
  }
  if (operationStart === null) operationStart = Date.parse(items[0].started_at) || Date.now();
  if (!ticker) ticker = setInterval(render, 1000);
  const title = stageText(current, "title");
  const text = stageText(current, "text");
  const { line: queue, share } = measure(current);
  const percent = share === null ? "" : `${Math.floor(share * 100)}%`;
  const time = t("progress.elapsed", { time: elapsed() });
  if (parked) {
    overlay?.remove(); overlay = null;
    const bar = ensureStrip();
    bar.querySelector(".progress-parking-text").textContent = [title, percent, queue || text, time].filter(Boolean).join(" · ");
    bar.querySelector(".progress-parking-show").textContent = t("progress.show");
    return;
  }
  strip?.remove(); strip = null;
  const box = ensureOverlay();
  box.querySelector(".progress-title").textContent = title;
  box.querySelector(".progress-text").textContent = text;
  box.querySelector(".progress-queue").textContent = queue;
  const meter = box.querySelector(".progress-bar");
  meter.hidden = share === null;
  meter.querySelector("span").style.width = share === null ? "0" : `${(share * 100).toFixed(1)}%`;
  box.querySelector(".progress-elapsed").textContent = time;
  const background = box.querySelector(".progress-background");
  background.textContent = t("progress.background");
  background.hidden = Date.now() - operationStart < BACKGROUND_AFTER_MS;
}

export function initializeProgressOverlay(options = {}) {
  context = { ...context, ...options };
  Events.On("filees:progress", (event) => {
    const snapshot = event?.data ?? event;
    items = Array.isArray(snapshot?.items) ? snapshot.items : [];
    render();
  });
  window.addEventListener("filees:language-changed", render);
}
