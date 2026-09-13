import { Events, Window } from "/wails/runtime.js";
import { GUIService } from "./bindings/filees/cmd/filees-gui-wails/index.js";
import { initializeTheme, setThemePreference } from "./theme-preference.js";
import { initializeLanguage, t, tn, getLocale } from "./i18n.js";
import { readRepoView, saveRepoView, repoSection, repoOrder } from "./repo-view.js";
import { initializeLanguageMenu } from "./language-menu.js";
import { shelvesFor, unparentedShelves } from "./shelf-layout.js";

initializeTheme();
initializeLanguage();
initializeLanguageMenu();

// Local Wails presentation event, not daemon IPC. Only the main window sends
// the resolved language; secondary windows follow the existing preference.
const syncNativeLanguage = () => Events.Emit("filees:native-language", getLocale())
  .catch(error => console.debug("Native language synchronization failed", error));
window.addEventListener("filees:language-changed", syncNativeLanguage);
syncNativeLanguage();

const $ = (selector) => document.querySelector(selector);
const escapeHTML = (value) => String(value ?? "")
  .replaceAll("&", "&amp;")
  .replaceAll("<", "&lt;")
  .replaceAll(">", "&gt;")
  .replaceAll('"', "&quot;")
  .replaceAll("'", "&#039;");

const localizedStates = new Set(["active", "busy", "initializing", "baselining", "paused", "stopping", "offline", "attention", "unattached", "disabled", "revoked", "unknown", "deleted"]);

const actionErrors = {
  get actions_unavailable() { return t("error.actionsUnavailable"); },
  get action_unavailable() { return t("error.actionUnavailable"); },
  get action_queue_busy() { return t("error.queueBusy"); },
};

let currentSnapshot = null;
let selectedDeletedCopy = null;
let deletedCopyReturnFocus = null;
let selectedAnnouncementID = "";
let announcementAckPending = "";
let announcementReturnFocus = null;
const selectedPublicShares = new Set();
let selectedPublicShareServer = "";
const renderedHTML = new WeakMap();
const expandedServers = new Set();
const seenServers = new Set();
const initialWindowWidth = 1180;
const autoFit = {
  enabled: true,
  appliedWidth: initialWindowWidth,
  running: false,
  queued: false,
  suppressResizeUntil: 0,
};

function replaceHTMLIfChanged(node, html) {
  if (renderedHTML.get(node) === html) return false;
  renderedHTML.set(node, html);
  node.innerHTML = html;
  return true;
}

function recordNumber(value, key) {
  return Number(value?.[key] ?? value?.[key.toLowerCase()] ?? 0);
}

function renderedTextWidth(node) {
  if (!node) return 0;
  const range = document.createRange();
  range.selectNodeContents(node);
  const width = range.getBoundingClientRect().width;
  range.detach?.();
  return width;
}

function prepareRepositoryWidths() {
  const rows = [...document.querySelectorAll(".repo-row")];
  const compactMinimum = window.innerWidth <= 800 ? 220 : window.innerWidth <= 900 ? 230 : 240;
  document.querySelectorAll(".server-folders").forEach((panel) => {
    const panelRows = [...panel.querySelectorAll(".repo-row")];
    const titleWidth = panelRows.reduce((largest, row) => {
      const open = row.querySelector(".repo-open");
      const name = row.querySelector(".repo-name strong");
      // Only the user-facing folder name defines the identity column. The
      // secondary absolute path is allowed to ellipsise inside that width;
      // treating it as a minimum used to push the realm counter and size
      // column outside an otherwise wide enough full-screen panel.
      // A block's scrollWidth includes the grid width assigned during the
      // previous pass. Feeding it back into --repo-title-min made the panel
      // grow on every daemon snapshot. A DOM range measures only the rendered
      // glyphs, so identical repository names now produce a stable minimum.
      const nameWidth = renderedTextWidth(name);
      const natural = (open?.getBoundingClientRect().width || 0) + nameWidth + 9;
      return Math.max(largest, Math.ceil(natural));
    }, compactMinimum);
    const actionsWidth = panelRows.reduce((largest, row) => {
      const tools = row.querySelector(".repo-tools");
      if (!tools) return largest;
      const buttons = [...tools.children];
      const natural = buttons.reduce((total, button) => total + button.getBoundingClientRect().width, 0)
        + Math.max(0, buttons.length - 1) * 5;
      return Math.max(largest, Math.ceil(natural));
    }, 0);
    // Folder names are identifiers, not prose. Keep them whole and let this
    // surface scroll if necessary; only the subordinate path may ellipsise.
    // Actions reserve only buttons that exist in this server panel.
    panel.style.setProperty("--repo-title-min", `${titleWidth}px`);
    panel.style.setProperty("--repo-actions-column", `${Math.max(44, actionsWidth)}px`);
  });
  return rows;
}

function repositoryOverflow(rows) {
  const rowOverflow = rows.reduce((largest, row) => Math.max(largest, row.scrollWidth - row.clientWidth), 0);
  const panelOverflow = [...document.querySelectorAll(".server-folders")]
    .reduce((largest, panel) => Math.max(largest, panel.scrollWidth - panel.clientWidth), 0);
  return Math.max(rowOverflow, panelOverflow);
}

function scheduleWindowFit() {
  if (!autoFit.enabled || autoFit.queued) return;
  autoFit.queued = true;
  window.requestAnimationFrame(() => {
    autoFit.queued = false;
    fitWindowToRepositories();
  });
}

async function fitWindowToRepositories() {
  if (!autoFit.enabled || autoFit.running) return;
  const rows = prepareRepositoryWidths();
  if (!rows.length) return;
  const overflow = repositoryOverflow(rows);
  if (overflow <= 2) return;

  autoFit.running = true;
  let resized = false;
  try {
    const [maximised, fullscreen, size, screen, position] = await Promise.all([
      Window.IsMaximised(), Window.IsFullscreen(), Window.Size(), Window.GetScreen(), Window.RelativePosition(),
    ]);
    if (maximised || fullscreen) return;

    const currentWidth = recordNumber(size, "Width");
    const currentHeight = recordNumber(size, "Height");
    const workWidth = recordNumber(screen?.WorkArea, "Width");
    const maxWidth = Math.max(currentWidth, workWidth - 24);
    if (!currentWidth || !currentHeight || currentWidth >= maxWidth - 2) return;

    // The repository column receives roughly two thirds of additional window
    // width while the activity column is visible.  A bounded iterative pass
    // accounts for both that grid ratio and different platform decorations.
    const targetWidth = Math.min(maxWidth, currentWidth + Math.max(24, Math.ceil(overflow * 1.6) + 12));
    if (targetWidth <= currentWidth + 2) return;

    autoFit.suppressResizeUntil = Date.now() + 700;
    autoFit.appliedWidth = Math.max(autoFit.appliedWidth, targetWidth);
    await Window.SetSize(targetWidth, currentHeight);
    resized = true;

    // Growing preserves the upper-left corner.  Keep the new right edge in
    // the current monitor's work area without disturbing a safely placed
    // window.
    const x = recordNumber(position, "X");
    const y = recordNumber(position, "Y");
    const maxX = Math.max(0, workWidth - targetWidth);
    if (x > maxX) await Window.SetRelativePosition(maxX, Math.max(0, y));
  } catch (error) {
    console.warn("Nie udało się dopasować szerokości panelu FileES", error);
  } finally {
    autoFit.running = false;
    // One more layout pass makes the measurement exact after the CSS grid has
    // redistributed the newly available width.
    if (resized) window.setTimeout(scheduleWindowFit, 80);
  }
}

function bytes(value) {
  const amount = Number(value || 0);
  if (amount < 1024) return `${amount} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let scaled = amount / 1024;
  let unit = units[0];
  for (let i = 1; scaled >= 1024 && i < units.length; i += 1) {
    scaled /= 1024;
    unit = units[i];
  }
  return `${scaled.toLocaleString(getLocale(), { maximumFractionDigits: scaled < 10 ? 1 : 0 })} ${unit}`;
}

function dateTime(value) {
  if (!value) return t("time.notRefreshed");
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return t("time.stateAt", { time: date.toLocaleTimeString(getLocale(), { hour: "2-digit", minute: "2-digit", second: "2-digit" }) });
}

function shortDateTime(value) {
  if (!value) return "czas nieznany";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(getLocale(), { day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit" });
}

// dataAge answers the question the header used to answer with the wrong
// measurement: how old is what we are showing, per server.
//
// The daemon link and the projection are two different facts, and the header
// used to report the first while naming the second. On 2026-09-02 every
// repository on `manual` rendered as current while its view had been refused
// for ten days - the interface was telling the truth about a connection nobody
// had asked about.
//
// The reservation emission is not consulted here on purpose. It answers its
// own question honestly and is already rendered in the reservations panel, but
// the same server can be fresh there and refused here because the two travel
// different SSH commands. Reading one as the other is the mistake this
// replaces, only inverted.
// A detached server is deliberately NOT rendered in the connection header.
//
// A detachment is something the owner did on purpose, confirming three times,
// and a banner naming the server afterwards is neither news nor help: nothing
// about re-activation can be done from the client, so the sentence is pure
// clutter. It can be worse than clutter - the name of a server and of its
// repositories staying on screen after someone deliberately cut ties with it is
// an exposure, not an untidiness, and no wording fixes that.
//
// snapshot.servers never carries a detached server any more: the reducer
// removes it and its repositories entirely. What arrives instead is
// snapshot.detachments, which is the moment rather than the flag, and it is
// rendered only in the collapsed card below and in the journal.

function ageInWords(value) {
  const at = Date.parse(value || "");
  if (!Number.isFinite(at)) return "";
  const minutes = Math.floor((Date.now() - at) / 60000);
  if (minutes < 2) return t("time.justNow");
  if (minutes < 90) return new Intl.RelativeTimeFormat(getLocale()).format(-minutes, "minute");
  const hours = Math.floor(minutes / 60);
  if (hours < 36) return new Intl.RelativeTimeFormat(getLocale()).format(-hours, "hour");
  const days = Math.floor(hours / 24);
  return new Intl.RelativeTimeFormat(getLocale()).format(-days, "day");
}

// Presentation only: chronology and grouping remain the host's projection.
function journalTime(item, now = new Date()) {
  const date = new Date(item.timestamp || "");
  if (!Number.isFinite(date.getTime())) return item.relative_time || "";
  const delta = now - date;
  if (delta < 60000) return t("time.justNow");
  if (delta < 600000) return new Intl.RelativeTimeFormat(getLocale()).format(-Math.floor(delta / 60000), "minute");
  if (date.toDateString() === now.toDateString()) return date.toLocaleTimeString(getLocale(), {hour: "2-digit", minute: "2-digit"});
  const calendarDay = d => Date.UTC(d.getFullYear(), d.getMonth(), d.getDate());
  const days = Math.round((calendarDay(now) - calendarDay(date)) / 86400000);
  return new Intl.RelativeTimeFormat(getLocale(), {numeric: "auto"}).format(-days, "day");
}

function renderConnection(snapshot) {
  const core = $("#pulse-core");
  const freshness = $("#projection-freshness");
  core.className = "pulse-core";
  freshness.className = "projection-freshness";
  let connectionLabel = t("fresh.offlineLabel");
  const projection = snapshot.projection || { state: "daemon_offline" };
  if (projection.state === "daemon_offline") {
    core.classList.add("is-offline");
    freshness.classList.add("is-unverified");
    freshness.textContent = t("fresh.offline");
  } else if (projection.state === "server_unverified" || projection.state === "server_unavailable") {
    core.classList.add("is-stale");
    freshness.classList.add("is-unverified");
    const age = ageInWords(projection.since);
    const rest = projection.additional_servers > 0 ? ` (+${projection.additional_servers})` : "";
    if (projection.state === "server_unverified") {
      freshness.textContent = age
        ? t("fresh.unverifiedAge", { name: projection.server_name, age, rest })
        : t("fresh.unverified", { name: projection.server_name, rest });
      connectionLabel = t("fresh.firstCheck");
    } else {
      freshness.textContent = age
        ? t("fresh.unavailableAge", { name: projection.server_name, age, rest })
        : t("fresh.unavailable", { name: projection.server_name, rest });
      connectionLabel = projection.reason ? `${projection.server_name}: ${projection.reason}` : t("server.health.unavailable");
    }
  } else if (projection.state === "refreshing") {
    core.classList.add("is-stale");
    freshness.classList.add("is-refreshing");
    freshness.textContent = t("fresh.refreshing");
    connectionLabel = t("fresh.refreshingLabel");
  } else {
    core.classList.add("is-online");
    freshness.classList.add("is-current");
    freshness.textContent = t("fresh.current");
    connectionLabel = t("fresh.currentLabel");
  }
  $("#offline").hidden = Boolean(snapshot.connected);
  $("#offline-copy").textContent = snapshot.last_refresh
    ? t("fresh.cached", { date: shortDateTime(snapshot.last_refresh) })
    : t("fresh.noCache");
  $("#pulse-card").dataset.connection = !snapshot.connected || projection.state === "daemon_offline"
    ? "offline"
    : projection.state === "current" ? "online" : "stale";
  $("#pulse-card").dataset.connectionLabel = connectionLabel;
}

function renderMetrics(snapshot) {
  const repos = snapshot.repositories || [];
  const pending = repos.reduce((sum, repo) => sum + Number(repo.pending_files || 0), 0);
  const pendingBytes = repos.reduce((sum, repo) => sum + Number(repo.pending_bytes || 0), 0);
  const conflicts = repos.reduce((sum, repo) => sum + Number(repo.conflicts || 0), 0);
  const reservations = snapshot.reservations || [];
  const reservationState = snapshot.reservation_status || { state: "daemon_offline", unavailable: [], offline: [], stale: [] };
  const reservationsOffline = reservationState.state === "daemon_offline";
  const reservationsPartial = reservationState.state === "partial";
  const publicShares = snapshot.public_shares || [];
  const activePublicShares = publicShares.filter((share) => share.state === "active").length;
  const unreadAnnouncements = (snapshot.notices || []).filter((notice) => !notice.acked).length;
  const attention = conflicts + unreadAnnouncements + (snapshot.errors?.length || 0);

  $("#metric-servers").textContent = snapshot.servers?.length ?? 0;
  $("#metric-repos").textContent = repos.length;
  $("#metric-pending").textContent = pending;
  $("#metric-servers-note").textContent = t("summary.activations");
  $("#metric-repos-note").textContent = t("summary.inView");
  $("#metric-attention-note").textContent = t("summary.notes");
  $("#metric-pending-note").textContent = pending ? t("summary.pendingBytes", { size: bytes(pendingBytes) }) : t("summary.emptyQueue");
  $("#metric-reservations").textContent = reservationsOffline
    ? reservations.length
    : reservationsPartial ? `${reservations.length}+?` : reservations.length;
  $("#metric-reservations-note").textContent = reservationsOffline
    ? t("summary.lastKnown")
    : reservationsPartial
    ? t("summary.partialLocks", { count: reservations.length, locks: tn("count.locks", reservations.length), missing: reservationState.unavailable.length })
    : tn("count.locks", reservations.length);
  $("#metric-public-shares").textContent = snapshot.public_shares_known ? activePublicShares : "?";
  $("#metric-public-shares-note").textContent = reservationsOffline && snapshot.public_shares_known
    ? t("summary.lastKnown")
    : snapshot.public_shares_known
      ? tn("count.links", activePublicShares)
      : t("summary.listUnavailable");
  $("#metric-attention").textContent = attention;
  $("#pulse-value").textContent = repos.length;
  $("#pulse-label").textContent = tn("count.repos", repos.length);
  const connectionLabel = $("#pulse-card").dataset.connectionLabel || t("pulse.unknown");
  if (!snapshot.connected) {
    $("#pulse-card").title = t("pulse.unverified", { connection: connectionLabel });
  } else if (snapshot.stale) {
    $("#pulse-card").title = t("pulse.stale", { connection: connectionLabel });
  } else {
    $("#pulse-card").title = attention > 0
      ? t("pulse.attention", { connection: connectionLabel, count: attention })
      : t("pulse.current", { connection: connectionLabel });
  }
  $("#hero-copy").textContent = snapshot.connected
    ? t("hero.live")
    : snapshot.last_refresh
      ? t("hero.cached")
      : t("hero.noCache");
}

function plural(value, one, few, many) {
  const number = Math.abs(Number(value || 0));
  if (number === 1) return one;
  const mod10 = number % 10;
  const mod100 = number % 100;
  return mod10 >= 2 && mod10 <= 4 && !(mod100 >= 12 && mod100 <= 14) ? few : many;
}

function retentionCountdown(value) {
  const deadline = Date.parse(value || "");
  if (!Number.isFinite(deadline)) return "termin nieznany";
  let seconds = Math.max(0, Math.floor((deadline - Date.now()) / 1000));
  if (seconds <= 0) return t("time.expired");
  const days = Math.floor(seconds / 86400); seconds %= 86400;
  const hours = Math.floor(seconds / 3600); seconds %= 3600;
  const minutes = Math.floor(seconds / 60); seconds %= 60;
  const clock = [hours, minutes, seconds].map((part) => String(part).padStart(2, "0")).join(":");
  return days ? t("time.daysClock", { days, clock }) : clock;
}

function updateRetentionCountdowns() {
  document.querySelectorAll("[data-retain-until]").forEach((node) => {
    node.textContent = retentionCountdown(node.dataset.retainUntil);
  });
}

const repoIcons = {
  info: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7v1"/></svg>',
  lock: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10" width="14" height="10" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>',
  unlock: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10" width="14" height="10" rx="2"/><path d="M9 10V7a4 4 0 0 1 7.5-2"/></svg>',
  publish: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 11v2a2 2 0 0 0 2 2h2l4 4V5L7 9H5a2 2 0 0 0-2 2Z"/><path d="M15 8a5 5 0 0 1 0 8M18 5a9 9 0 0 1 0 14"/></svg>',
  recovery: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v12M7 10l5 5 5-5M5 20h14"/></svg>',
  remove: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M9 7V4h6v3M7 7l1 13h8l1-13M10 11v5M14 11v5"/></svg>',
  quarantine: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3 20 6v5c0 5-3.4 8.3-8 10-4.6-1.7-8-5-8-10V6l8-3Z"/><path d="M9 9l6 6M15 9l-6 6"/></svg>',
  pin: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 3h6l-1 5 3 3v2H7v-2l3-3-1-5M12 13v8"/></svg>',
  settings: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.08A1.7 1.7 0 0 0 8.97 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.52-1H3v-4h.08A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 8.97 4.6 1.7 1.7 0 0 0 10 3.08V3h4v.08A1.7 1.7 0 0 0 15.03 4.6a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9a1.7 1.7 0 0 0 1.52 1H21v4h-.08A1.7 1.7 0 0 0 19.4 15Z"/></svg>',
};

function repoAction(action, label, icon, extraClass = "") {
  return `<button class="repo-icon-action hint-button ${extraClass}" type="button" data-action="${escapeHTML(action)}" data-hint="${escapeHTML(label)}" aria-label="${escapeHTML(label)}">${icon}</button>`;
}

// Sentences are composed here, not carried across the contract. The daemon
// sends a stable token and a detail; the wording belongs to the interface, the
// same rule errcat keys follow.
//
// None of these mention letter case on purpose. A person told "these differ
// only in case" has been told about Windows; they need to be told about their
// own two documents and what to do next.
const unportableReasons = {
  case_collision: (detail) => t("name.caseCollision", { detail }),
  reserved_device: () => t("name.device"),
  reserved_rune: (detail) => t("name.rune", { detail }),
  control_rune: () => t("name.control"),
  separator: (detail) => t("name.separator", { detail }),
  trailing_dot_or_space: (detail) => t("name.trailing", { detail }),
  empty: () => t("name.empty"),
};

// The condition is a state of the folder, not a message: it stays until the
// cause is gone and there is nothing to acknowledge. Dismissing it would leave
// the object outside FileES for good, in silence — the failure this whole gate
// exists to remove.
function renderUnportable(repo) {
  const names = repo.unportable_names || [];
  if (!names.length) return "";
  const items = names
    .map((entry) => {
      const reason = (unportableReasons[entry.kind] || (() => t("name.unknown")))(entry.detail || "");
      return `<li><code>${escapeHTML(entry.path)}</code><span>${escapeHTML(reason)}</span><button class="unportable-rename" type="button" data-action="rename_unportable" data-path="${escapeHTML(entry.path)}">${escapeHTML(t("name.rename"))}</button></li>`;
    })
    .join("");
  return `<div class="repo-unportable"><strong>${escapeHTML(t("name.excluded", { count: names.length }))}</strong><ul>${items}</ul></div>`;
}

function renderRepo(repo) {
  const state = repo.display_state || "unknown";
  const deleted = Boolean(repo.server_deleted);
  const localProvisioning = Boolean(repo.local_provisioning);
  const pending = deleted
    ? (repo.local_copy_preserved ? (repo.local_cleanup_pending ? t("queue.deletedCleanup") : repo.local_copy_status === "clean" ? t("queue.deletedClean") : repo.local_copy_status === "changed" ? t("queue.deletedChanged") : t("queue.deletedCheck")) : repo.recovery_pending && repo.local_cleanup_pending ? t("queue.archiveCleanup") : repo.recovery_pending ? t("queue.archive") : repo.local_cleanup_pending ? t("queue.cleanup") : t("queue.detached"))
    : localProvisioning
      ? (state === "attention" ? t("queue.importAttention") : state === "offline" ? t("queue.importOffline") : t("queue.importRunning"))
    : (repo.pending_files ? `${repo.pending_files} · ${bytes(repo.pending_bytes)}` : t("queue.empty"));
  const source = repo.local_path || t(repo.attached ? "repo.folder" : "repo.remote");
  const actions = [
    deleted && repo.local_copy_preserved ? `<button class="repo-icon-action hint-button" type="button" data-copy-info data-hint="${escapeHTML(t("copy.info"))}" aria-label="${escapeHTML(t("copy.info"))}" aria-haspopup="dialog" aria-controls="deleted-copy-dialog">` + repoIcons.info + `</button>` : "",
    repo.can_attach ? repoAction("attach_repository", t("repo.attach"), repoIcons.pin, "attach") : "",
    repo.recovery_available ? repoAction("download_recovery", t("repo.recovery"), repoIcons.recovery, "recovery") : "",
    repo.can_dismiss_recovery ? repoAction("dismiss_recovery", t("repo.dismissRecovery"), repoIcons.remove, "recovery-dismiss") : "",
    repo.can_review_quarantine ? repoAction("review_quarantine", t("repo.quarantine"), repoIcons.quarantine, "quarantine") : "",
    repo.can_lock ? repoAction("lock", t("repo.lock"), repoIcons.lock, "mutate") : "",
    repo.can_unlock ? repoAction("unlock", t("repo.unlock"), repoIcons.unlock, "mutate") : "",
    repo.can_publish ? repoAction("publish", t("repo.publish"), repoIcons.publish, "publish") : "",
  ].join("");
  const stateLabel = localizedStates.has(state) ? t(`state.${state}`) : state;
  const disconnected = repo.connectivity !== "online" || ["offline", "unattached", "disabled", "revoked", "unknown"].includes(state);
  const stateOverlay = disconnected
    ? '<span class="repo-state-overlay" aria-hidden="true"><svg viewBox="0 0 16 16"><path d="M3 3l10 10M5.2 10.8 3.8 12.2a2 2 0 0 1-2.8-2.8l2.1-2.1M10.8 5.2l1.4-1.4A2 2 0 0 1 15 6.6l-2.1 2.1"/></svg></span>'
    : state === "attention"
      ? '<span class="repo-state-overlay is-attention" aria-hidden="true">!</span>'
      : state === "busy" || state === "initializing" || state === "baselining" || localProvisioning
        ? '<span class="repo-state-overlay is-busy" aria-hidden="true"></span>'
        : "";
  const iconContents = `<span class="repo-icon" aria-hidden="true">${stateOverlay}</span>`;
  const open = repo.can_open
    ? `<button class="repo-open hint-button state-${escapeHTML(state)}" type="button" data-action="open_folder" data-hint="${escapeHTML(t("repo.open", { state: stateLabel }))}" aria-label="${escapeHTML(t("repo.open", { state: stateLabel }))}">${iconContents}</button>`
    : `<span class="repo-open is-disabled state-${escapeHTML(state)}" title="${escapeHTML(stateLabel)}" aria-label="${escapeHTML(stateLabel)}">${iconContents}</span>`;
  const settings = deleted ? "" : repoAction("settings", t("repo.settings"), repoIcons.settings, "repo-settings");
  const size = repo.attached && repo.working_copy_size_known && Number.isFinite(Number(repo.working_copy_bytes ?? 0))
    ? bytes(repo.working_copy_bytes ?? 0)
    : "—";
  return `<article class="repo-row ${repo.intent_resolution_required ? "requires-decision" : ""}" data-repo-id="${escapeHTML(repo.id)}">
    <div class="repo-title">
      ${open}
      <div class="repo-name"><strong title="${escapeHTML(repo.display_name)}">${escapeHTML(repo.display_name || repo.id)}</strong><small title="${escapeHTML(source)}">${escapeHTML(source)}</small></div>
    </div>
    <div class="repo-meta repo-queue"><small>${escapeHTML(t(deleted ? "repo.localState" : "repo.queue"))}</small><span title="${escapeHTML(deleted ? repo.cleanup_error : "")}">${escapeHTML(pending)}</span></div>
    <div class="repo-tools">${settings}${actions}</div>
    <div class="repo-meta repo-size"><small>${escapeHTML(t("repo.size"))}</small><span>${escapeHTML(size)}</span></div>
    ${renderUnportable(repo)}
    ${repo.intent_resolution_required ? `<div class="intent-folder-warning"><strong>${escapeHTML(t("intent.required"))}</strong><button type="button" data-action="settings">${escapeHTML(t("intent.resolve"))}</button></div>` : ""}
  </article>`;
}

const expandedIdleGroups = new Set();
function renderRepoGroup(label, repos, className = "", nested = false) {
  if (!repos.length) return "";
  repos = [...repos].sort(repoOrder);
  if (!nested && ["owned", "guest", "unclassified"].includes(className)) {
    const prefs = readRepoView();
    const groups = {active:[],inactive:[],archived:[]};
    for (const repo of repos) {
      const needsAttention = (currentSnapshot?.notices || []).some(n => n.repo_id === repo.id && !n.acked);
      groups[needsAttention ? "active" : repoSection(repo,prefs)].push(repo);
    }
    const fold = (kind, title) => {
      const items = groups[kind]; if (!items.length) return "";
      const key = JSON.stringify([repos[0].server_id,className,kind]);
      return `<details class="idle-group" data-idle-key="${escapeHTML(key)}" ${expandedIdleGroups.has(key)?"open":""}><summary>${escapeHTML(title)} (${items.length})</summary>${renderRepoGroup(label,items,className,true)}</details>`;
    };
    return renderRepoGroup(label,groups.active,className,true)
      + fold("inactive",t("folders.inactiveAfter", { days: prefs.inactive }))
      + fold("archived",t("folders.archived"));
  }
  return `<section class="realm-group ${escapeHTML(className)}">
    <div class="realm-divider"><span>${escapeHTML(label)}</span><b>${repos.length}</b></div>
    <div class="repo-list">${repos.map(repo => {
      const shelves = shelvesFor(repo, currentSnapshot?.repositories || []);
      const shelfKey = JSON.stringify([repo.server_id, repo.id, "shelves"]);
      return renderRepo(repo) + (shelves.length ? `<details class="parent-shelves idle-group" data-idle-key="${escapeHTML(shelfKey)}" ${expandedIdleGroups.has(shelfKey) ? "open" : ""}><summary>${escapeHTML(t("repo.groupShelves"))} (${shelves.length})</summary>${renderRepoGroup(t("repo.groupShelves"), shelves, "upload-shelf", true)}</details>` : "");
    }).join("")}</div>
  </section>`;
}

function serverHealthPresentation(value) {
  switch (value) {
    case "current": return { className: "health-current", label: t("server.health.current") };
    case "refreshing": return { className: "health-refreshing", label: t("server.health.refreshing") };
    case "server_unavailable": return { className: "health-unavailable", label: t("server.health.unavailable") };
    case "daemon_offline": return { className: "health-unavailable", label: t("server.health.offline") };
    default: return { className: "health-unverified", label: t("server.health.unverified") };
  }
}

function renderRepositories(snapshot) {
  const root = $("#repositories");
  const repos = snapshot.repositories || [];
  const servers = [...(snapshot.servers || [])];
  if (!servers.length && !repos.length) {
    return replaceHTMLIfChanged(root, `<div class="empty-state"><span>◌</span><p>${escapeHTML(t("repo.empty"))}</p></div>`);
  }
  const known = new Set(servers.map((server) => server.id));
  repos.forEach((repo) => {
    if (!known.has(repo.server_id)) {
      servers.push({ id: repo.server_id, display_name: repo.server_id, address: "" });
      known.add(repo.server_id);
    }
  });
  servers.forEach((server) => {
    if (seenServers.has(server.id)) return;
    seenServers.add(server.id);
    expandedServers.add(server.id);
  });
  const html = servers.map((server) => {
    const serverRepos = repos.filter((repo) => repo.server_id === server.id);
    const isShelf = (repo) => repo.purpose === "upload_shelf";
    const isTrash = (repo) => repo.purpose === "upload_trash";
    const deleted = serverRepos.filter((repo) => repo.server_deleted);
    const live = serverRepos.filter((repo) => !repo.server_deleted);
    const shelves = unparentedShelves(live);
    const trash = live.filter(isTrash);
    const rest = live.filter((repo) => !isShelf(repo) && !isTrash(repo));
    // Initial repository creation owns a local source folder before it can
    // truthfully claim an attached SVN working copy. Keep that row in its
    // local realm group while the daemon performs the first commit.
    const attached = rest.filter((repo) => repo.attached || repo.local_provisioning);
    const remote = rest.filter((repo) => !repo.attached && !repo.local_provisioning);
    const owned = attached.filter((repo) => repo.ownership === "owned");
    const guest = attached.filter((repo) => repo.ownership === "guest");
    const unclassified = attached.filter((repo) => !["owned", "guest"].includes(repo.ownership));
    const context = server.realm_alias || server.address || server.id;
    const expanded = expandedServers.has(server.id);
    const health = serverHealthPresentation(server.health);
    const attention = serverRepos.some((repo) => repo.display_state === "attention" || Number(repo.conflicts || 0) > 0)
      || (snapshot.errors || []).some((error) => serverRepos.some((repo) => repo.id === error.repo_id))
      || (snapshot.notices || []).some((notice) => !notice.acked && serverRepos.some((repo) => repo.id === notice.repo_id));
    const accent = /^#[0-9a-f]{6}$/i.test(server.accent_color || "") ? server.accent_color : "#FF6A00";
    return `<article class="server-panel ${attention ? "has-attention" : ""}" data-server-id="${escapeHTML(server.id)}" style="--realm-accent:${escapeHTML(accent)}">
      <header class="server-header" data-toggle-server="${escapeHTML(server.id)}" tabindex="0" role="button" aria-expanded="${expanded}" aria-controls="server-folders-${escapeHTML(server.id)}">
        <div class="server-identity"><span class="server-mark ${health.className}" role="img" aria-label="${escapeHTML(health.label)}" title="${escapeHTML(health.label)}"></span><div>
          <div class="server-title-line">
            <h3>${escapeHTML(server.display_name || server.id)}</h3>
            <button class="server-settings" type="button" data-action="settings" title="${escapeHTML(t("server.settings"))}" aria-label="${escapeHTML(t("server.settingsName", { name: server.display_name || server.id }))}">
              <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.08A1.7 1.7 0 0 0 8.97 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.52-1H3v-4h.08A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 8.97 4.6 1.7 1.7 0 0 0 10 3.08V3h4v.08A1.7 1.7 0 0 0 15.03 4.6a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9a1.7 1.7 0 0 0 1.52 1H21v4h-.08A1.7 1.7 0 0 0 19.4 15Z"></path></svg>
            </button>
          </div>
          <p title="${escapeHTML(context)}">${escapeHTML(context)}</p>
        </div></div>
        <div class="server-summary"><span class="server-total">${escapeHTML(tn("count.folders", serverRepos.length))}</span><span class="server-chevron" aria-hidden="true">⌄</span></div>
      </header>
      <div id="server-folders-${escapeHTML(server.id)}" class="server-folders" ${expanded ? "" : "hidden"}>
        ${serverRepos.length ? `<div class="repo-columns" aria-hidden="true"><span>${escapeHTML(t("repo.columnFolder"))}</span><span class="column-queue">${escapeHTML(t("repo.queue"))}</span><span>${escapeHTML(t("repo.actions"))}</span><span>${escapeHTML(t("repo.size"))}</span></div>
          ${renderRepoGroup(t("repo.groupOwned"), owned, "owned")}
          ${renderRepoGroup(t("repo.groupGuest"), guest, "guest")}
          ${renderRepoGroup(t("repo.groupShelves"), shelves, "upload-shelf")}
          ${renderRepoGroup(t("repo.groupQuarantine"), trash, "upload-trash")}
          ${renderRepoGroup(t("repo.groupOther"), unclassified, "unclassified")}
          ${renderRepoGroup(t("repo.groupDeleted"), deleted, "deleted")}
          ${renderRepoGroup(t("repo.groupRemote"), remote, "remote")}` : `<p class="server-empty">${escapeHTML(t("server.empty"))}</p>`}
      </div>
    </article>`;
  }).join("");
  return replaceHTMLIfChanged(root, html);
}

function renderActions(snapshot) {
  const root = $("#action-status");
  const actions = snapshot.pending_actions || [];
  root.hidden = actions.length === 0;
  const html = actions.map((action) => {
    const repo = (snapshot.repositories || []).find((item) => item.id === action.repo_id);
    const scope = repo?.display_name || action.repo_id || "FileES";
    const detail = !snapshot.connected
      ? t("progress.connection")
      : action.phase === "awaiting_projection"
      ? t("progress.projection")
      : t("progress.running");
    return `<article class="action-badge">
      <span class="action-spinner" aria-hidden="true"></span>
      <div><strong>${escapeHTML(action.label || t("progress.action"))}</strong><small>${escapeHTML(scope)} · ${escapeHTML(detail)}</small></div>
    </article>`;
  }).join("");
  replaceHTMLIfChanged(root, html);
}

function renderReservations(snapshot) {
  const card = $("#reservations-card");
  const root = $("#reservations");
  const reservations = snapshot.reservations || [];
	const holderRequests = (snapshot.lock_release_requests || []).filter((request) => request.role === "holder" && request.state === "pending");
  const reservationState = snapshot.reservation_status || { state: "daemon_offline", unavailable: [], offline: [], stale: [] };
  const reservationsOffline = reservationState.state === "daemon_offline";
  const reservationsPartial = reservationState.state === "partial";
	card.hidden = reservations.length === 0 && holderRequests.length === 0 && !reservationsPartial && !reservationsOffline && reservationState.offline.length === 0 && reservationState.stale.length === 0;
  $("#reservations-count").textContent = reservationsPartial ? `${reservations.length}+?` : reservations.length;
	const availabilityHTML = reservationsPartial
		? `<p class="muted">${escapeHTML(t("locks.partial", { servers: reservationState.unavailable.map((server) => server.display_name || server.id || t("locks.server")).join(", ") }))}</p>`
		: reservationsOffline && (reservations.length || holderRequests.length)
		? `<p class="muted">${escapeHTML(t("locks.cached"))}</p>`
		: reservationsOffline
		? `<p class="muted">${escapeHTML(t("locks.unverified"))}</p>`
		: reservationState.offline.length
		? `<p class="muted">${escapeHTML(t("locks.offline", { servers: reservationState.offline.map((server) => server.display_name || server.id || t("locks.server")).join(", ") }))}</p>`
		: reservationState.stale.length
		? `<p class="muted">${escapeHTML(t("locks.stale", { servers: reservationState.stale.map((server) => server.display_name || server.id || t("locks.server")).join(", ") }))}</p>`
		: "";
	if (!reservations.length && !holderRequests.length) {
    replaceHTMLIfChanged(root, availabilityHTML);
    return;
  }
	const requestsHTML = holderRequests.map((request) => `<article class="reservation-row lock-release-request" data-lock-release-request-id="${escapeHTML(request.id)}">
		<div class="reservation-main">
			<strong title="${escapeHTML(request.path)}">${escapeHTML(t("locks.requestPath", { path: request.path || t("locks.file") }))}</strong>
			<p>${escapeHTML(request.counterparty_realm_alias || t("locks.otherPerson"))} · ${escapeHTML(request.repository || request.repo_id)}</p>
			<div class="lock-flags"><span class="lock-flag request">${escapeHTML(t("locks.releaseRequest"))}</span><span>${escapeHTML(shortDateTime(request.created_at))}</span></div>
		</div>
		<div class="reservation-request-actions">
			${request.can_dismiss ? '<button class="reservation-action secondary" data-action="dismiss_lock_release">OK</button>' : ""}
			${request.can_accept ? `<button class="reservation-action" data-action="accept_lock_release">${escapeHTML(t("locks.release"))}</button>` : ""}
		</div>
	</article>`).join("");
	const reservationsHTML = reservations.map((reservation) => {
    const flags = [
      reservation.active_passport ? `<span class="lock-flag passport">${escapeHTML(t("locks.passport"))}</span>` : "",
      reservation.local_changes ? `<span class="lock-flag risk">${escapeHTML(t("locks.localChanges"))}</span>` : "",
    ].join("");
		let action = `<span class="lock-owner">${escapeHTML(t("locks.otherOwner"))}</span>`;
		if (reservation.can_release) action = `<button class="reservation-action" data-action="release_reservation">${escapeHTML(t("locks.release"))}</button>`;
		else if (reservation.can_request_release) action = `<button class="reservation-action" data-action="request_lock_release">${escapeHTML(t("locks.requestRelease"))}</button>`;
		else if (reservation.lock_release_state === "pending") action = `<span class="lock-owner waiting">${escapeHTML(t("locks.requestSent"))}</span>`;
		else if (reservation.lock_release_state === "dismissed") action = `<span class="lock-owner">${escapeHTML(t("locks.kept"))}</span>`;
		else if (reservation.lock_release_state === "accepted") action = `<span class="lock-owner waiting">${escapeHTML(t("locks.releasing"))}</span>`;
    return `<article class="reservation-row" data-reservation-id="${escapeHTML(reservation.id)}">
      <div class="reservation-main">
        <strong title="${escapeHTML(reservation.path)}">${escapeHTML(reservation.path || t("locks.file"))}</strong>
        <p>${escapeHTML(reservation.repository || reservation.repo_id)} · ${escapeHTML(reservation.owner_label || t("locks.unknownOwner"))}</p>
        <div class="lock-flags">${flags}<span>${escapeHTML(shortDateTime(reservation.created_at))}</span></div>
      </div>
      ${action}
    </article>`;
	}).join("");
	replaceHTMLIfChanged(root, availabilityHTML + requestsHTML + reservationsHTML);
}

function publicShareState(state) {
  if (state === "active") return t("share.active");
  if (state === "revoked") return t("share.revoked");
  if (state === "deleted") return t("share.deleted");
  return state || t("share.unknown");
}

function renderPublicShares(snapshot) {
  const card = $("#public-shares-card");
  const root = $("#public-shares");
  const shares = snapshot.public_shares || [];
  card.hidden = !snapshot.public_shares_known || shares.length === 0;
  if (card.hidden) {
    replaceHTMLIfChanged(root, "");
    return;
  }
  const active = shares.filter((share) => share.state === "active").length;
  $("#public-shares-count").textContent = active === shares.length ? active : `${active}/${shares.length}`;
  const servers = new Map((snapshot.servers || []).map((server) => [server.id, server.display_name || server.realm_alias || server.id]));
  const activeShares = new Map(shares.filter((share) => share.can_revoke).map((share) => [share.channel_id, share]));
  for (const channelID of selectedPublicShares) {
    const share = activeShares.get(channelID);
    if (!share || (selectedPublicShareServer && share.server_id !== selectedPublicShareServer)) selectedPublicShares.delete(channelID);
  }
  if (!selectedPublicShares.size) selectedPublicShareServer = "";
  const activeByServer = new Map();
  for (const share of activeShares.values()) {
    if (!activeByServer.has(share.server_id)) activeByServer.set(share.server_id, []);
    activeByServer.get(share.server_id).push(share.channel_id);
  }
  const bulk = selectedPublicShares.size
    ? `<div class="dashboard-share-bulk"><span>${escapeHTML(t("share.selected", { count: selectedPublicShares.size }))}</span><button type="button" data-share-bulk>${escapeHTML(t("share.bulk"))}</button></div>`
    : "";
  let previousServer = "";
  const rows = shares.map((share) => {
    const serverChannels = activeByServer.get(share.server_id) || [];
    const serverHeader = share.server_id !== previousServer
      ? `<div class="dashboard-share-group" data-server-id="${escapeHTML(share.server_id)}"><span>${escapeHTML(servers.get(share.server_id) || share.server_id || t("share.server"))}</span>${serverChannels.length ? `<button type="button" data-share-revoke-all data-channel-ids="${escapeHTML(serverChannels.join(","))}">${escapeHTML(t("share.all"))}</button>` : ""}</div>`
      : "";
    previousServer = share.server_id;
    const activeShare = share.state === "active";
    const scope = share.follow_head ? t("share.follow") : t("share.frozen");
    const audience = share.recipient_count
      ? t("share.recipients", { count: share.recipient_count })
      : t("share.open");
    const objects = t("share.files", { count: share.object_count });
    const manage = share.can_open
      ? `<button class="dashboard-share-open" type="button" data-action="manage_public_shares" aria-label="${escapeHTML(t("share.openName", { name: share.address }))}">
          <span class="dashboard-share-dot ${activeShare ? "active" : ""}" aria-hidden="true"></span>
          <span class="dashboard-share-copy"><strong>${escapeHTML(share.address || t("share.default"))}</strong><small>${escapeHTML(share.repository)} · ${escapeHTML(objects)} · ${escapeHTML(scope)}</small><time>${escapeHTML(shortDateTime(share.updated_at))}</time></span>
        </button>`
      : `<div class="dashboard-share-open is-disabled"><span class="dashboard-share-dot ${activeShare ? "active" : ""}" aria-hidden="true"></span><span class="dashboard-share-copy"><strong>${escapeHTML(share.address || t("share.default"))}</strong><small>${escapeHTML(share.repository)} · ${escapeHTML(objects)} · ${escapeHTML(scope)}</small><time>${escapeHTML(shortDateTime(share.updated_at))}</time></span></div>`;
    const revoke = share.can_revoke ? `<button class="dashboard-share-revoke" type="button" data-action="revoke_public_share">${escapeHTML(t("share.revoke"))}</button>` : "";
    return `${serverHeader}<article class="dashboard-share-row ${activeShare ? "is-active" : "is-inactive"}" data-server-id="${escapeHTML(share.server_id)}" data-repo-id="${escapeHTML(share.repo_id)}" data-channel-id="${escapeHTML(share.channel_id)}">
      <label class="dashboard-share-select" title="${escapeHTML(t("share.select"))}">${share.can_revoke ? `<input type="checkbox" data-share-select ${selectedPublicShares.has(share.channel_id) ? "checked" : ""}><span aria-hidden="true"></span>` : ""}</label>${manage}<div class="dashboard-share-policy"><span>${escapeHTML(publicShareState(share.state))} · ${escapeHTML(audience)}</span><small>${escapeHTML(t("share.lifetime"))}</small></div>${revoke}
    </article>`;
  }).join("");
  replaceHTMLIfChanged(root, bulk + rows);
}

function unreadAnnouncements(snapshot) {
  return (snapshot?.notices || []).filter((notice) => !notice.acked);
}

function renderAnnouncementBanner(snapshot) {
  const unread = unreadAnnouncements(snapshot);
  const unresolved = (snapshot.repositories || []).filter(repo => repo.intent_resolution_required);
  const alerts = $("#intent-alerts");
  alerts.hidden = unresolved.length === 0;
  // This is persistent state from IPC, not an event toast or notice ACK.
  // Stable markup avoids re-announcing the same decision on every tick.
  replaceHTMLIfChanged(alerts, unresolved.map(repo => `<div class="announcement-banner" data-repo-id="${escapeHTML(repo.id)}" data-server-id="${escapeHTML(repo.server_id)}"><div><strong>${escapeHTML(t("intent.paused", { name: repo.display_name || repo.id }))}</strong><p>${escapeHTML(t("intent.help"))}</p></div><button type="button" data-action="settings">${escapeHTML(t("intent.resolve"))}</button></div>`).join(""));
  const banner = $("#announcement-banner");
  banner.hidden = unread.length === 0;
  $("#top").classList.toggle("has-announcements", unread.length > 0 || unresolved.length > 0);
  const heroTitle = unresolved.length ? "hero.waitTitle" : unread.length ? "hero.shoutTitle" : "hero.title";
  const heroAccent = unresolved.length ? "hero.waitAccent" : unread.length ? "hero.shoutAccent" : "hero.accent";
  replaceHTMLIfChanged($("#hero-title"), `${escapeHTML(t(heroTitle))}<br><span>${escapeHTML(t(heroAccent))}</span>`);
  $("#announcement-banner-count").textContent = unread.length
    ? tn("count.unread", unread.length)
    : "";
}

function renderShouts(snapshot) {
  const card = $("#shouts-card");
  const root = $("#shouts");
  const notices = snapshot.notices || [];
  const unread = notices.filter((notice) => !notice.acked).length;
  card.hidden = notices.length === 0;
  card.classList.toggle("has-unread", unread > 0);
  $("#shouts-count").textContent = unread
    ? tn("count.unread", unread)
    : t("shout.read");
  if (!notices.length) {
    replaceHTMLIfChanged(root, "");
    renderAnnouncementDialog(snapshot);
    return;
  }
  const repositories = new Map((snapshot.repositories || []).map((repo) => [repo.id, repo.display_name || repo.id]));
  // Never let recent acknowledged history hide an older unread announcement.
  const visible = [...unreadAnnouncements(snapshot), ...notices.filter((notice) => notice.acked).slice(0, 5)];
  const html = visible.map((notice) => {
    const repository = repositories.get(notice.repo_id) || notice.repo_id || "FileES";
    const revision = Number(notice.revision || 0) > 0 ? ` · r${Number(notice.revision)}` : "";
    const state = notice.acked ? t("shout.read") : t("shout.review");
    return `<button class="shout-row ${notice.acked ? "is-read" : "is-unread"}" type="button" data-notice-id="${escapeHTML(notice.id)}" aria-label="${escapeHTML(t("shout.open", { title: notice.title }))}">
      <span class="shout-symbol" aria-hidden="true">${repoIcons.publish}</span>
      <span class="shout-main"><strong>${escapeHTML(notice.title || t("shout.default"))}</strong>
      <span>${escapeHTML(repository + revision)}</span><time>${escapeHTML(t("shout.received", { date: shortDateTime(notice.created_at) }))}</time></span>
      <span class="shout-state">${state}</span>
    </button>`;
  }).join("");
  replaceHTMLIfChanged(root, html);
  renderAnnouncementDialog(snapshot);
}

function announcementScope(snapshot, notice) {
  const repo = (snapshot.repositories || []).find((item) => item.id === notice.repo_id);
  const server = (snapshot.servers || []).find((item) => item.id === repo?.server_id);
  const repository = repo?.display_name || notice.repo_id || "FileES";
  const serverName = server?.display_name || server?.realm_alias || server?.id || "";
  return serverName ? `${serverName} · ${repository}` : repository;
}

function closeAnnouncement() {
  $("#announcement-overlay").hidden = true;
  selectedAnnouncementID = "";
  announcementAckPending = "";
  const target = announcementReturnFocus;
  announcementReturnFocus = null;
  if (target?.isConnected) target.focus();
  else if (!$("#announcement-banner").hidden) $("#open-announcements").focus();
  else $("#client-version").focus();
}

function openAnnouncement(noticeID, focusOrigin = null) {
  const notice = (currentSnapshot?.notices || []).find((item) => item.id === noticeID);
  if (!notice) return;
  selectedAnnouncementID = noticeID;
  announcementReturnFocus = focusOrigin;
  renderAnnouncementDialog(currentSnapshot);
  $("#announcement-overlay").hidden = false;
  (notice.can_ack ? $("#ack-announcement") : $("#close-announcement")).focus();
}

function openNewestUnreadAnnouncement(focusOrigin = null) {
  const notices = currentSnapshot?.notices || [];
  const notice = notices.find((item) => !item.acked) || notices[0];
  if (notice) openAnnouncement(notice.id, focusOrigin);
}

function openNextUnreadAnnouncement() {
  const unread = unreadAnnouncements(currentSnapshot);
  if (!unread.length) return;
  const index = unread.findIndex((notice) => notice.id === selectedAnnouncementID);
  openAnnouncement(unread[(index + 1) % unread.length].id, announcementReturnFocus);
}

function renderAnnouncementDialog(snapshot) {
  if (!selectedAnnouncementID) return;
  const notice = (snapshot.notices || []).find((item) => item.id === selectedAnnouncementID);
  if (notice && announcementAckPending === notice.id && notice.acked) {
    announcementAckPending = "";
    const next = unreadAnnouncements(snapshot)[0];
    if (next) {
      openAnnouncement(next.id, announcementReturnFocus);
      return;
    }
    closeAnnouncement();
    return;
  }
  if (!notice) {
    closeAnnouncement();
    return;
  }
  $("#announcement-copy").textContent = notice.title || t("shout.default");
  $("#announcement-repository").textContent = announcementScope(snapshot, notice);
  const revision = $("#announcement-revision");
  revision.hidden = !(Number(notice.revision || 0) > 0);
  revision.textContent = revision.hidden ? "" : t("shout.revision", { revision: Number(notice.revision) });
  $("#announcement-time").textContent = t("shout.received", { date: shortDateTime(notice.created_at) });
  const unread = unreadAnnouncements(snapshot);
  $("#announcement-status").textContent = notice.acked ? t("shout.acked") : t("shout.unreadHelp", { count: unread.length });
  $("#next-announcement").hidden = unread.length < 2;
  $(".announcement-dialog .eyebrow").textContent = notice.acked ? t("shout.read") : t("shout.attention");
  const ack = $("#ack-announcement");
  ack.hidden = !notice.can_ack;
  ack.disabled = announcementAckPending === notice.id;
  ack.textContent = ack.disabled ? t("shout.acking") : t("shout.ack");
}

async function acknowledgeAnnouncement() {
  const notice = (currentSnapshot?.notices || []).find((item) => item.id === selectedAnnouncementID);
  if (!notice?.can_ack || announcementAckPending) return;
  announcementAckPending = notice.id;
  renderAnnouncementDialog(currentSnapshot);
  try {
    const result = await GUIService.Trigger({ kind: "ack_notice", notice_id: notice.id });
    if (!result.accepted) {
      announcementAckPending = "";
      renderAnnouncementDialog(currentSnapshot);
      showToast({ level: "normal", title: t("shout.cannotAck"), message: actionErrors[result.code] || result.code });
      return;
    }
    window.setTimeout(() => {
      if (announcementAckPending !== notice.id) return;
      announcementAckPending = "";
      renderAnnouncementDialog(currentSnapshot);
    }, 8000);
  } catch (error) {
    announcementAckPending = "";
    renderAnnouncementDialog(currentSnapshot);
    showToast({ level: "critical", title: t("shout.ackFailed"), message: error?.message || String(error) });
  }
}

// renderDetached shows relationships that have ended, for about forty-eight
// hours after they do.
//
// Every judgement here was made in Go and arrives finished: the sentence, the
// relative time, whether anything is left to do. This function decides nothing
// and remembers nothing - it has no way to check any of it, and a panel that
// answers a question it cannot ask is a panel that lies. Note in particular
// that the lifetime is not applied here: the daemon owns it, and a second
// opinion about time in the frontend would disagree with the first the moment
// either was wrong.
function renderDetached(snapshot) {
  const records = snapshot.detachments || [];
  const card = $("#detached-card");
  card.hidden = records.length === 0;
  $("#detached-count").textContent = String(records.length);
  if (!records.length) {
    replaceHTMLIfChanged($("#detached"), "");
    return;
  }
  replaceHTMLIfChanged($("#detached"), records.map((item) => {
    const paths = item.working_copies || [];
    // The folders are the answer to the only question left: the files stayed
    // on this disk, and this is where they are.
    const folders = paths.length
      ? `<ul class="detached-paths">${paths.map((path) => `<li title="${escapeHTML(path)}">${escapeHTML(path)}</li>`).join("")}</ul>`
      : "";
    const note = item.needs_reactivation
      ? `<p class="detached-note">${escapeHTML(t("detached.reactivate"))}</p>`
      : "";
    return `<article class="detached-row">
      <div class="detached-head"><strong>${escapeHTML(t(item.needs_reactivation ? "detached.remote" : "detached.self", {name: item.name || item.server_id}))}</strong>
      <time datetime="${escapeHTML(item.timestamp || item.exact_time)}">${escapeHTML(journalTime(item))}</time></div>
      ${note}${folders}
    </article>`;
  }).join(""));
}

// A sentence whose count has to agree with a noun arrives as a catalogue key
// and its arguments, because the rule for choosing the form belongs to the
// language and lives here, in Intl.PluralRules. Everything else arrives as one
// finished sentence the host already resolved. The plain text stays the
// fallback, so an entry from an older host still reads.
function journalSummary(item) {
  const message = item.summary_message;
  return message ? t(message.key, message.args || {}) : (item.summary || "");
}

// Details may be several finished sentences. They are joined, never built from
// fragments: each one is a whole catalogue entry a translator can reorder.
function journalDetails(item) {
  const messages = item.details_messages || [];
  return messages.length ? messages.map(message => t(message.key, message.args || {})).join(" ") : (item.details || "");
}

function renderJournal(snapshot) {
  const entries = snapshot.journal || [];
  const root = $("#activity");
  if (!entries.length) {
    replaceHTMLIfChanged(root, `<p class="muted">${escapeHTML(t("journal.empty"))}</p>`);
  } else {
    replaceHTMLIfChanged(root, entries.slice(0, 6).map((item) => `<article class="activity-row ${item.emphasized ? "is-error" : ""}">
      <span class="activity-dot"></span><div><strong title="${escapeHTML(journalSummary(item))}">${escapeHTML(journalSummary(item))}</strong>
      <p>${escapeHTML(item.repository || "FileES")}</p><time datetime="${escapeHTML(item.timestamp || item.exact_time)}">${escapeHTML(journalTime(item))}</time></div>
    </article>`).join(""));
  }

  const full = $("#journal");
  replaceHTMLIfChanged(full, entries.length ? entries.map((item) => `<article class="journal-row ${item.emphasized ? "is-error" : ""}">
    <time>${escapeHTML(item.exact_time)}</time>
    <span class="journal-repo">${escapeHTML(item.repository || "FileES")}</span>
    <div class="journal-copy"><strong>${escapeHTML(journalSummary(item))}</strong>${journalDetails(item) ? `<p>${escapeHTML(journalDetails(item))}</p>` : ""}${item.diagnostics ? `<pre class="journal-diagnostics">${escapeHTML(item.diagnostics)}</pre>` : ""}</div>
  </article>`).join("") : `<p class="muted">${escapeHTML(t("journal.noEntries"))}</p>`);
}

function render(snapshot) {
  if (!snapshot) return;
  currentSnapshot = snapshot;
  renderDeletedCopyDialog();
  const clientVersion = String(snapshot.client_version || "").trim();
  const versionBadge = $("#client-version");
  versionBadge.textContent = clientVersion || "—";
  versionBadge.title = clientVersion ? `Wersja klienta FileES ${clientVersion}` : "Wersja klienta FileES jest nieznana";
  renderVersionDialog(snapshot);
  const pairButton = $("#pair-mobile");
  const capabilities = new Set(snapshot.capabilities || []);
  pairButton.disabled = !snapshot.connected || snapshot.stale || !(snapshot.servers || []).length || !capabilities.has("mobile_pairing.begin");
  renderConnection(snapshot);
  const memory = snapshot.memory_safety;
  const memoryBanner = $("#memory-safety");
  const memoryPhases = ["warning", "draining", "deferred", "cooldown", "restarting", "recovery_required"];
  memoryBanner.hidden = !memoryPhases.includes(memory?.phase);
  memoryBanner.textContent = memoryBanner.hidden ? "" : t(`memory.${memory.phase}`);
  renderMetrics(snapshot);
  const repositoriesChanged = renderRepositories(snapshot);
  renderActions(snapshot);
  renderReservations(snapshot);
  renderPublicShares(snapshot);
  renderAnnouncementBanner(snapshot);
  renderShouts(snapshot);
	renderUpdate(snapshot);
  renderDetached(snapshot);
  renderJournal(snapshot);
  $("#last-refresh").textContent = dateTime(snapshot.last_refresh);
  $("#revision").textContent = t("projection.revision", { revision: snapshot.revision || 0 });
  if (repositoriesChanged) scheduleWindowFit();
  updateRetentionCountdowns();
}

function renderVersionDialog(snapshot) {
  const clientVersion = String(snapshot?.client_version || "").trim();
  const update = snapshot?.update;
  const channel = String(update?.channel || "").trim();
  const currentRelease = String(update?.current_version || "").trim();
  const availableRelease = String(update?.available_version || "").trim();
  const available = Boolean(availableRelease) && update?.state !== "current";
  $("#version-client").textContent = clientVersion || t("version.unknownClient");
  $("#version-channel").textContent = channel || t("version.unknownChannel");
  $("#version-release").textContent = currentRelease || t("version.unknownRelease");
  if (!update) {
    $("#version-status").textContent = t("version.noUpdateInfo");
  } else if (update.state === "restart_required") {
    $("#version-status").textContent = update.summary || t("version.restartSummary");
  } else if (available) {
    $("#version-status").textContent = update.summary || t("version.availableSummary", { available: availableRelease, current: currentRelease || t("version.unknownRelease") });
  } else {
    $("#version-status").textContent = update.summary || t("version.currentSummary");
  }
  $("#version-update-actions").hidden = !available || update?.state === "restart_required";
  $("#version-restart-actions").hidden = update?.state !== "restart_required";
}

function openVersionDialog() {
  renderVersionDialog(currentSnapshot);
  $("#version-overlay").hidden = false;
  $("#close-version").focus();
}

function closeVersionDialog() {
  $("#version-overlay").hidden = true;
  $("#client-version").focus();
}

function renderUpdate(snapshot) {
	const card = $("#update-card");
	const update = snapshot.update;
	const restart = update?.state === "restart_required" && update.restart_required;
	const available = Boolean(update?.available_version) && update.state !== "current";
	card.hidden = !available && !restart;
	if (card.hidden) return;
	$("#update-title").textContent = t(restart ? "version.restartTitle" : "version.availableTitle");
	$("#update-actions").hidden = restart;
	$("#update-restart-actions").hidden = !restart;
	$("#update-version").textContent = update.available_version;
	$("#update-summary").textContent = update.summary || t("version.installedSummary", { current: update.current_version || t("version.unknownClient") });
}

window.setInterval(updateRetentionCountdowns, 1000);

function showToast(feedback) {
  const root = $("#toasts");
  const toast = document.createElement("article");
  const level = feedback?.level || "normal";
  toast.className = `toast ${level === "critical" ? "critical" : level === "normal" ? "normal" : "low"}`;
  toast.innerHTML = `<strong>${escapeHTML(feedback?.title || "FileES")}</strong>${feedback?.message ? `<span>${escapeHTML(feedback.message)}</span>` : ""}`;
  root.appendChild(toast);
  window.setTimeout(() => {
    toast.classList.add("is-leaving");
    window.setTimeout(() => toast.remove(), 220);
  }, level === "critical" ? 8000 : 4800);
}

function deletedCopyRepo() {
  return (currentSnapshot?.repositories || []).find((repo) =>
    repo.id === selectedDeletedCopy?.repoID && repo.server_id === selectedDeletedCopy?.serverID && repo.server_deleted && repo.local_copy_preserved);
}

function renderDeletedCopyDialog() {
  if (!selectedDeletedCopy) return;
  const repo = deletedCopyRepo();
  if (!repo) {
    $("#deleted-copy-dialog").close();
    return;
  }
  $("#deleted-copy-name").textContent = repo.display_name || repo.id;
  $("#deleted-copy-path").textContent = repo.local_path || t("copy.noPath");
  $("#deleted-copy-description").textContent = t("copy.description");
  $("#deleted-copy-status").textContent = repo.local_copy_status === "clean"
    ? t("copy.clean")
    : repo.local_copy_status === "changed"
      ? t("copy.changed")
      : t("copy.unverified");
  $("#deleted-copy-cleanup").textContent = repo.local_cleanup_pending
    ? t("copy.cleanupPending")
    : t("copy.cleanupDone");
  $("#deleted-copy-diagnostics").hidden = !repo.cleanup_error;
  $("#deleted-copy-error").textContent = repo.cleanup_error || "";
  $("#detach-deleted-copy").disabled = !repo.can_detach_local_copy;
  $("#deleted-copy-action-help").textContent = repo.can_detach_local_copy
    ? t("copy.detachHelp")
    : repo.local_cleanup_pending
      ? t("copy.waitCleanup")
      : t("copy.waitDaemon");
}

function openDeletedCopyInfo(button) {
  const row = button.closest("[data-repo-id]");
  const server = button.closest("[data-server-id]");
  selectedDeletedCopy = { repoID: row?.dataset.repoId, serverID: server?.dataset.serverId };
  if (!deletedCopyRepo()) { selectedDeletedCopy = null; return; }
  deletedCopyReturnFocus = button;
  renderDeletedCopyDialog();
  $("#deleted-copy-dialog").showModal();
  $("#close-deleted-copy").focus();
}

async function detachDeletedCopy() {
  const repo = deletedCopyRepo();
  if (!repo?.can_detach_local_copy) return;
  // The action controller rechecks the current state before and after its
  // owned confirmation. This dialog is explanatory, not a second authority.
  $("#deleted-copy-dialog").close();
  try {
    const result = await GUIService.Trigger({ kind: "detach_repository", repo_id: repo.id, server_id: repo.server_id });
    if (!result.accepted) showToast({ level: "normal", title: t("ui.actionUnavailable"), message: actionErrors[result.code] || result.code });
  } catch (error) {
    showToast({ level: "critical", title: t("ui.intentFailed"), message: error?.message || String(error) });
  }
}

async function triggerAction(button) {
  const repoRow = button.closest("[data-repo-id]");
  const reservationRow = button.closest("[data-reservation-id]");
	const lockReleaseRow = button.closest("[data-lock-release-request-id]");
  const noticeRow = button.closest("[data-notice-id]");
	const publicShareRow = button.closest("[data-channel-id]");
  const serverPanel = button.closest("[data-server-id]");
	const globalAction = button.closest("[data-global-action]");
	if (!repoRow && !reservationRow && !lockReleaseRow && !noticeRow && !serverPanel && !globalAction) return;
  button.disabled = true;
  try {
    const result = await GUIService.Trigger({
      kind: button.dataset.action,
      repo_id: repoRow?.dataset.repoId || "",
      server_id: serverPanel?.dataset.serverId || "",
      reservation_id: reservationRow?.dataset.reservationId || "",
		lock_release_request_id: lockReleaseRow?.dataset.lockReleaseRequestId || "",
      notice_id: noticeRow?.dataset.noticeId || "",
		channel_id: publicShareRow?.dataset.channelId || "",
      path: button.dataset.path || "",
    });
    if (!result.accepted) {
      showToast({ level: "normal", title: t("ui.actionUnavailable"), message: actionErrors[result.code] || result.code });
    }
  } catch (error) {
    showToast({ level: "critical", title: t("ui.intentFailed"), message: error?.message || String(error) });
  } finally {
    window.setTimeout(() => { button.disabled = false; }, 450);
  }
}

async function triggerBulkPublicShares(button, serverID, channelIDs) {
  const cleanIDs = [...new Set(channelIDs.filter(Boolean))];
  if (!serverID || !cleanIDs.length) return;
  button.disabled = true;
  try {
    const result = await GUIService.Trigger({ kind: "revoke_public_shares", server_id: serverID, channel_ids: cleanIDs });
    if (!result.accepted) {
      showToast({ level: "normal", title: t("ui.actionUnavailable"), message: actionErrors[result.code] || result.code });
      return;
    }
    selectedPublicShares.clear();
    selectedPublicShareServer = "";
    renderPublicShares(currentSnapshot);
  } catch (error) {
    showToast({ level: "critical", title: t("ui.intentFailed"), message: error?.message || String(error) });
  } finally {
    window.setTimeout(() => { button.disabled = false; }, 450);
  }
}

async function invoke(button, action) {
  button.disabled = true;
  try {
    await action();
  } finally {
    window.setTimeout(() => { button.disabled = false; }, 500);
  }
}

Events.On("filees:snapshot", (event) => render(event?.data ?? event));
window.addEventListener("filees:language-changed", () => {
  if (currentSnapshot) render(currentSnapshot);
  if (selectedDeletedCopy) renderDeletedCopyDialog();
  document.querySelectorAll("[data-toggle-card]").forEach(updateCardToggleLabel);
  scheduleWindowFit();
});
Events.On("filees:action-feedback", (event) => showToast(event?.data ?? event));
Events.On("filees:open-announcement", openNewestUnreadAnnouncement);
$("#activate").addEventListener("click", (event) => triggerAction(event.currentTarget));
$("#pair-mobile").addEventListener("click", (event) => triggerAction(event.currentTarget));
function updateThemeToggle() {
  const preference = document.documentElement.dataset.themePreference || "system";
  document.querySelectorAll("[data-theme-preference]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.themePreference === preference));
  });
}
$("#theme-switch").addEventListener("click", (event) => {
  const button = event.target.closest("[data-theme-preference]");
  if (button) setThemePreference(button.dataset.themePreference);
});
window.addEventListener("filees:theme-changed", updateThemeToggle);
updateThemeToggle();
$("#refresh").addEventListener("click", (event) => invoke(event.currentTarget, GUIService.Refresh));
$("#reconnect").addEventListener("click", (event) => invoke(event.currentTarget, GUIService.Reconnect));
$(".side-column").addEventListener("click", (event) => {
  const toggle = event.target.closest("[data-toggle-card]");
  if (!toggle) return;
  const body = document.getElementById(toggle.dataset.toggleCard);
  if (!body) return;
  const expanded = toggle.getAttribute("aria-expanded") === "true";
  body.hidden = expanded;
  toggle.setAttribute("aria-expanded", String(!expanded));
  updateCardToggleLabel(toggle);
});
function updateCardToggleLabel(toggle) {
  const subjects = {"public-shares-body":"shares", "update-body":"update", "reservations-body":"locks", "shouts-body":"shouts", "detached-body":"detached", "activity-body":"activity"};
  const subject = subjects[toggle.dataset.toggleCard];
  if (!subject) return;
  const key = `${toggle.getAttribute("aria-expanded") === "true" ? "collapse" : "expand"}.${subject}`;
  toggle.setAttribute("data-i18n-title", key);
  toggle.setAttribute("data-i18n-aria-label", key);
  toggle.title = t(key);
  toggle.setAttribute("aria-label", t(key));
}
document.querySelectorAll("[data-toggle-card]").forEach(updateCardToggleLabel);
$("#open-journal").addEventListener("click", () => {
  $("#journal-overlay").hidden = false;
  $("#close-journal").focus();
});
$("#close-journal").addEventListener("click", () => { $("#journal-overlay").hidden = true; });
$("#journal-overlay").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) event.currentTarget.hidden = true;
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Tab" && !$("#announcement-overlay").hidden && !$("#deleted-copy-dialog").open) {
    const buttons = [...document.querySelectorAll("#announcement-overlay button")].filter((button) => !button.hidden && !button.disabled);
    const first = buttons[0], last = buttons[buttons.length - 1];
    if (event.shiftKey && (document.activeElement === first || !$("#announcement-overlay").contains(document.activeElement))) {
      event.preventDefault(); last?.focus();
    } else if (!event.shiftKey && (document.activeElement === last || !$("#announcement-overlay").contains(document.activeElement))) {
      event.preventDefault(); first?.focus();
    }
    return;
  }
  if (event.key !== "Escape") return;
  if ($("#deleted-copy-dialog").open) return; // native dialog handles Escape
  if (!$("#announcement-overlay").hidden) {
    closeAnnouncement();
    return;
  }
  if (!$("#version-overlay").hidden) {
    closeVersionDialog();
    return;
  }
  if (!$("#journal-overlay").hidden) $("#journal-overlay").hidden = true;
});
$("#repositories").addEventListener("click", (event) => {
  const info = event.target.closest("[data-copy-info]");
  if (info) { openDeletedCopyInfo(info); return; }
  const toggle = event.target.closest("[data-toggle-server]");
  if (toggle && !event.target.closest("button")) {
    const serverID = toggle.dataset.toggleServer;
    if (expandedServers.has(serverID)) expandedServers.delete(serverID);
    else expandedServers.add(serverID);
    if (renderRepositories(currentSnapshot)) scheduleWindowFit();
    return;
  }
  const button = event.target.closest("[data-action]");
  if (button) triggerAction(button);
});
$("#repositories").addEventListener("keydown", (event) => {
  if (event.target.closest("button")) return;
  const toggle = event.target.closest("[data-toggle-server]");
  if (!toggle || !["Enter", " "].includes(event.key)) return;
  event.preventDefault();
  const serverID = toggle.dataset.toggleServer;
  if (expandedServers.has(serverID)) expandedServers.delete(serverID);
  else expandedServers.add(serverID);
  if (renderRepositories(currentSnapshot)) scheduleWindowFit();
});
$("#intent-alerts").addEventListener("click", (event) => {
  const button = event.target.closest("[data-action]");
  if (button) triggerAction(button);
});
$("#repositories").addEventListener("toggle", event => {
  const key = event.target.dataset?.idleKey; if (!key) return;
  if(event.target.open) expandedIdleGroups.add(key); else expandedIdleGroups.delete(key);
}, true);
function refreshRepoViewPreferences() {
  const prefs=readRepoView();
  $("#inactive-days").value=prefs.inactive;
  $("#archive-days").value=prefs.archive;
  if(currentSnapshot) { renderRepositories(currentSnapshot); scheduleWindowFit(); }
}
$("#save-repo-view").addEventListener("click",()=>{
  const inactive=Number($("#inactive-days").value), archive=Number($("#archive-days").value);
  if(![inactive,archive].every(n=>Number.isInteger(n)&&n>=0&&n<=36500)) {
    showToast({title:t("view.daysTitle"),message:t("view.daysInvalid"),level:"critical"});
    return;
  }
  try {
    saveRepoView({...readRepoView(),inactive,archive});
    $(".repo-view-preferences").open=false;
    $(".repo-view-preferences summary").focus();
    showToast({title:t("view.saved"),message:t("view.applied")});
    scheduleWindowFit();
  }
  catch { showToast({title:t("view.saveFailed"),level:"critical"}); }
});
window.addEventListener("storage",event=>{ if(event.key === "filees.repo-view.v1") refreshRepoViewPreferences(); });
window.addEventListener("filees:repo-view",refreshRepoViewPreferences);
refreshRepoViewPreferences();
$("#close-deleted-copy").addEventListener("click", () => $("#deleted-copy-dialog").close());
$("#dismiss-deleted-copy").addEventListener("click", () => $("#deleted-copy-dialog").close());
$("#detach-deleted-copy").addEventListener("click", detachDeletedCopy);
$("#deleted-copy-dialog").addEventListener("close", () => {
  selectedDeletedCopy = null;
  if (deletedCopyReturnFocus?.isConnected) deletedCopyReturnFocus.focus();
  else $("#client-version").focus();
  deletedCopyReturnFocus = null;
});
$("#reservations").addEventListener("click", (event) => {
  const button = event.target.closest("[data-action]");
  if (button) triggerAction(button);
});
$("#public-shares").addEventListener("click", (event) => {
  const all = event.target.closest("[data-share-revoke-all]");
  if (all) {
    triggerBulkPublicShares(all, all.closest("[data-server-id]")?.dataset.serverId || "", (all.dataset.channelIds || "").split(","));
    return;
  }
  const bulk = event.target.closest("[data-share-bulk]");
  if (bulk) {
    triggerBulkPublicShares(bulk, selectedPublicShareServer, [...selectedPublicShares]);
    return;
  }
  const button = event.target.closest("[data-action]");
  if (button) triggerAction(button);
});
$("#public-shares").addEventListener("change", (event) => {
  const checkbox = event.target.closest("[data-share-select]");
  if (!checkbox) return;
  const row = checkbox.closest("[data-channel-id]");
  const channelID = row?.dataset.channelId || "";
  const serverID = row?.dataset.serverId || "";
  if (checkbox.checked) {
    if (selectedPublicShareServer && selectedPublicShareServer !== serverID) selectedPublicShares.clear();
    selectedPublicShareServer = serverID;
    selectedPublicShares.add(channelID);
  } else {
    selectedPublicShares.delete(channelID);
    if (!selectedPublicShares.size) selectedPublicShareServer = "";
  }
  renderPublicShares(currentSnapshot);
});
$("#shouts").addEventListener("click", (event) => {
  const button = event.target.closest("[data-notice-id]");
  if (button) openAnnouncement(button.dataset.noticeId, button);
});
$("#close-announcement").addEventListener("click", closeAnnouncement);
$("#open-announcements").addEventListener("click", (event) => openNewestUnreadAnnouncement(event.currentTarget));
$("#next-announcement").addEventListener("click", openNextUnreadAnnouncement);
$("#dismiss-announcement").addEventListener("click", closeAnnouncement);
$("#ack-announcement").addEventListener("click", acknowledgeAnnouncement);
$("#announcement-overlay").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) closeAnnouncement();
});
$("#client-version").addEventListener("click", openVersionDialog);
$("#close-version").addEventListener("click", closeVersionDialog);
$("#dismiss-version").addEventListener("click", closeVersionDialog);
$("#version-overlay").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) closeVersionDialog();
});
$("#version-update-actions").addEventListener("click", (event) => {
  const button = event.target.closest("[data-action]");
  if (button) triggerAction(button);
});
$("#update-actions").addEventListener("click", (event) => {
	const button = event.target.closest("[data-action]");
	if (button) triggerAction(button);
});
$("#window-minimise").addEventListener("click", () => Window.Minimise());
$("#window-maximise").addEventListener("click", () => Window.ToggleMaximise());
// Closing the panel is a presentation gesture.  Stack shutdown remains the
// explicit, confirmed FileES lifecycle action in the tray.
$("#window-close").addEventListener("click", () => Window.Hide());
$("#titlebar").addEventListener("dblclick", (event) => {
  if (!event.target.closest(".topbar-actions")) Window.ToggleMaximise();
});

let manualResizeTimer = 0;
window.addEventListener("resize", () => {
  window.clearTimeout(manualResizeTimer);
  manualResizeTimer = window.setTimeout(async () => {
    if (Date.now() < autoFit.suppressResizeUntil) return;
    try {
      if (await Window.IsMaximised() || await Window.IsFullscreen()) return;
      const size = await Window.Size();
      const width = recordNumber(size, "Width");
      // A deliberate user shrink wins over automatic content fitting.  A
      // later enlargement remains eligible for future content-driven growth.
      if (width + 8 < autoFit.appliedWidth) autoFit.enabled = false;
      else {
        const resumed = !autoFit.enabled;
        autoFit.enabled = true;
        autoFit.appliedWidth = Math.max(autoFit.appliedWidth, width);
        if (resumed) scheduleWindowFit();
      }
    } catch (error) {
      console.debug("Nie udało się rozpoznać ręcznej zmiany rozmiaru", error);
    }
  }, 180);
});

try {
  render(await GUIService.Snapshot());
} catch (error) {
  console.error("Nie udało się pobrać projekcji FileES", error);
}
