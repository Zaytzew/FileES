import { Events, Window } from "/wails/runtime.js";
import { GUIService } from "./bindings/filees/cmd/filees-gui-wails/index.js";

const $ = (selector) => document.querySelector(selector);
const escapeHTML = (value) => String(value ?? "")
  .replaceAll("&", "&amp;")
  .replaceAll("<", "&lt;")
  .replaceAll(">", "&gt;")
  .replaceAll('"', "&quot;")
  .replaceAll("'", "&#039;");

const stateLabels = {
  active: "Aktywne", busy: "Praca", initializing: "Start", baselining: "Baza",
  paused: "Pauza", stopping: "Stop", offline: "Offline", attention: "Uwaga",
  unattached: "Bez kopii", disabled: "Wyłączone", revoked: "Cofnięte", unknown: "Nieznane",
  deleted: "Archiwum",
};

const actionErrors = {
  actions_unavailable: "Akcje systemowe są niedostępne w tym buildzie.",
  action_unavailable: "Ta akcja nie jest już dostępna. Stan repozytorium mógł się zmienić.",
  action_queue_busy: "Kolejka intencji GUI jest zajęta. Spróbuj ponownie.",
};

let currentSnapshot = null;
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
  return `${scaled.toLocaleString("pl-PL", { maximumFractionDigits: scaled < 10 ? 1 : 0 })} ${unit}`;
}

function dateTime(value) {
  if (!value) return "Jeszcze nie odświeżono";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return `Stan z ${date.toLocaleTimeString("pl-PL", { hour: "2-digit", minute: "2-digit", second: "2-digit" })}`;
}

function shortDateTime(value) {
  if (!value) return "czas nieznany";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString("pl-PL", { day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit" });
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
function staleViewServers(snapshot) {
  const servers = snapshot.servers || [];
  return servers
    .filter((server) => {
      if (Number(server.view_sync_failures || 0) > 0) return true;
      if (String(server.view_sync_error || "") !== "") return true;
      // Never fetched in this run. The daemon starts from a cached view, so
      // without this a freshly started client renders a ten-day-old projection
      // as current until the first sync happens to fail - which is the same
      // untruth this replaced, moved to startup. Absent is not the same as
      // fine, and only the daemon can tell us which.
      return String(server.view_synced_at || "") === "";
    })
    .map((server) => ({
      name: server.display_name || server.id,
      since: server.view_generated_at || server.view_synced_at || "",
      reason: String(server.view_sync_error || ""),
      unverified: String(server.view_synced_at || "") === "" && Number(server.view_sync_failures || 0) === 0,
    }));
}

// abandonedServers are the ones the daemon can reach and which have stopped
// publishing for us. This is the case no local measurement can see: fetching
// succeeds, the generation never moves, and only the age grows, so a quiet
// server and an abandoned one are identical from here. The server itself
// reports when it last produced our view, and that report is the only evidence
// there is.
//
// Reported as a fact rather than judged. How long is too long depends on the
// unproducedServers reports which servers have published nothing for a long
// while. It is deliberately NOT used by the connection header.
//
// The header answers one question - can I trust what I am looking at - and
// this fact cannot help answer it. We reach that header only after a
// successful fetch, so the server has just told us it is fine; and the client
// cannot tell "nothing changed" from "something changed and was not
// published", because it does not know what happens on the server. A number
// nobody can act on does not belong in a trust signal, and dressed as a
// warning it teaches people to ignore the header that will one day carry
// something true.
//
// It stays because the age of the last change is honest information about a
// realm's activity, and a details view is where such information belongs.
function unproducedServers(snapshot) {
  const servers = snapshot.servers || [];
  return servers
    .filter((server) => {
      if (String(server.server_view_produced_at || "") === "") return false;
      if (Number(server.view_sync_failures || 0) > 0) return false;
      const produced = Date.parse(server.server_view_produced_at);
      if (!Number.isFinite(produced)) return false;
      // Thirty days, not one. The server publishes only when something
      // changes - an activation, a pairing, a grant - and never on a timer, so
      // an old timestamp means nothing changed, which is the ordinary state of
      // a project between phases. Below a month, silence is indistinguishable
      // from a quiet week and saying anything is a false alarm.
      //
      // A server that has actually stopped answering shows up as a failed
      // sync, which is a different branch with its own wording. Silence alone
      // is never evidence of a fault.
      return Date.now() - produced > 30 * 24 * 3600 * 1000;
    })
    .map((server) => ({
      name: server.display_name || server.id,
      since: server.server_view_produced_at,
    }));
}

function ageInWords(value) {
  const at = Date.parse(value || "");
  if (!Number.isFinite(at)) return "";
  const minutes = Math.floor((Date.now() - at) / 60000);
  if (minutes < 2) return "sprzed chwili";
  if (minutes < 90) return `sprzed ${minutes} min`;
  const hours = Math.floor(minutes / 60);
  if (hours < 36) return `sprzed ${hours} ${plural(hours, "godziny", "godzin", "godzin")}`;
  const days = Math.floor(hours / 24);
  return `sprzed ${days} ${plural(days, "dnia", "dni", "dni")}`;
}

function renderConnection(snapshot) {
  const core = $("#pulse-core");
  const freshness = $("#projection-freshness");
  core.className = "pulse-core";
  freshness.className = "projection-freshness";
  let connectionLabel = "Demon jest rozłączony";
  const stale = snapshot.connected ? staleViewServers(snapshot) : [];
  if (!snapshot.connected) {
    core.classList.add("is-offline");
    freshness.classList.add("is-unverified");
    freshness.textContent = "Demon niedostępny — dane niepotwierdzone";
  } else if (stale.length > 0) {
    // Named, not counted. "One server is stale" sends the reader hunting; the
    // name and the age let them decide whether it matters to them.
    core.classList.add("is-stale");
    freshness.classList.add("is-unverified");
    const first = stale[0];
    const age = ageInWords(first.since);
    const rest = stale.length > 1 ? ` (+${stale.length - 1})` : "";
    // "Not yet checked" and "checked and refused" are different states and the
    // reader acts differently on them: the first usually resolves itself in
    // seconds, the second will not resolve at all.
    if (first.unverified) {
      freshness.textContent = age
        ? `Dane z „${first.name}" ${age} — jeszcze niesprawdzone${rest}`
        : `Dane z „${first.name}" jeszcze niesprawdzone${rest}`;
      connectionLabel = "Pierwsze sprawdzenie w toku";
    } else {
      freshness.textContent = age
        ? `Dane z „${first.name}" ${age} — serwer nie odpowiada${rest}`
        : `Dane z „${first.name}" niepotwierdzone — serwer nie odpowiada${rest}`;
      connectionLabel = first.reason ? `${first.name}: ${first.reason}` : "Serwer nie odpowiada";
    }
  } else if (snapshot.stale) {
    core.classList.add("is-stale");
    freshness.classList.add("is-refreshing");
    freshness.textContent = "Aktualizowanie danych";
    connectionLabel = "Demon odświeża projekcję";
  } else {
    core.classList.add("is-online");
    freshness.classList.add("is-current");
    freshness.textContent = "Stan danych: aktualny";
    connectionLabel = "Połączenie z demonem jest aktywne";
  }
  $("#offline").hidden = Boolean(snapshot.connected);
  $("#offline-copy").textContent = snapshot.last_refresh
    ? `Pokazujemy ostatnią pełną projekcję z ${shortDateTime(snapshot.last_refresh)}. Jej bieżącego stanu nie można zweryfikować; po odzyskaniu połączenia panel odświeży się automatycznie.`
    : "Nie ma jeszcze zapisanej pełnej projekcji. Po odzyskaniu połączenia panel odświeży się automatycznie.";
  $("#pulse-card").dataset.connection = snapshot.connected && !snapshot.stale ? "online" : snapshot.connected ? "stale" : "offline";
  $("#pulse-card").dataset.connectionLabel = connectionLabel;
}

function renderMetrics(snapshot) {
  const repos = snapshot.repositories || [];
  const pending = repos.reduce((sum, repo) => sum + Number(repo.pending_files || 0), 0);
  const pendingBytes = repos.reduce((sum, repo) => sum + Number(repo.pending_bytes || 0), 0);
  const conflicts = repos.reduce((sum, repo) => sum + Number(repo.conflicts || 0), 0);
  const reservations = snapshot.reservations || [];
  const reservationState = reservationProjectionState(snapshot);
  const publicShares = snapshot.public_shares || [];
  const activePublicShares = publicShares.filter((share) => share.state === "active").length;
  const unreadAnnouncements = (snapshot.notices || []).filter((notice) => !notice.acked).length;
  const attention = conflicts + unreadAnnouncements + (snapshot.errors?.length || 0);

  $("#metric-servers").textContent = snapshot.servers?.length ?? 0;
  $("#metric-repos").textContent = repos.length;
  $("#metric-pending").textContent = pending;
  $("#metric-pending-note").textContent = pending ? `${bytes(pendingBytes)} oczekuje` : "kolejka jest pusta";
  $("#metric-reservations").textContent = reservationState.daemonOffline
    ? reservations.length
    : reservationState.partial ? `${reservations.length}+?` : reservations.length;
  $("#metric-reservations-note").textContent = reservationState.daemonOffline
    ? "ostatni znany stan · demon offline"
    : reservationState.partial
    ? `co najmniej ${reservations.length} ${plural(reservations.length, "aktywna blokada", "aktywne blokady", "aktywnych blokad")} · ${reservationState.unavailable.length} bez emisji`
    : plural(reservations.length, "aktywna blokada", "aktywne blokady", "aktywnych blokad");
  $("#metric-public-shares").textContent = snapshot.public_shares_known ? activePublicShares : "?";
  $("#metric-public-shares-note").textContent = reservationState.daemonOffline && snapshot.public_shares_known
    ? "ostatni znany stan · demon offline"
    : snapshot.public_shares_known
      ? plural(activePublicShares, "aktywny link", "aktywne linki", "aktywnych linków")
      : "lista niedostępna";
  $("#metric-attention").textContent = attention;
  $("#pulse-value").textContent = repos.length;
  $("#pulse-label").textContent = plural(repos.length, "repozytorium", "repozytoria", "repozytoriów");
  $("#pulse-card").classList.toggle("has-attention", attention > 0);
  const connectionLabel = $("#pulse-card").dataset.connectionLabel || "Stan połączenia nieznany";
  if (!snapshot.connected) {
    $("#pulse-card").title = `${connectionLabel}. Projekcja jest niezweryfikowana.`;
  } else if (snapshot.stale) {
    $("#pulse-card").title = `${connectionLabel}. Dane nie są jeszcze bieżące.`;
  } else {
    $("#pulse-card").title = attention > 0
      ? `${connectionLabel}. ${attention} ${plural(attention, "uwaga", "uwagi", "uwag")} do sprawdzenia.`
      : `${connectionLabel}. Repozytoria nie wymagają uwagi.`;
  }
  $("#hero-copy").textContent = snapshot.connected
    ? "Zmiany i działania pojawiają się tutaj na bieżąco."
    : snapshot.last_refresh
      ? "Połączenie jest chwilowo niedostępne. Panel zachowuje ostatni znany stan i odświeży się automatycznie."
      : "Połączenie jest chwilowo niedostępne. Brak zapisanej projekcji; panel odświeży się automatycznie.";
}

function reservationProjectionState(snapshot) {
  const servers = snapshot.servers || [];
  const unavailable = snapshot.connected ? servers.filter((server) => !server.reservations_known || server.reservation_projection === "unknown") : [];
  const offline = snapshot.connected ? servers.filter((server) => server.reservation_projection === "offline") : [];
  const stale = snapshot.connected ? servers.filter((server) => server.reservation_projection === "stale") : [];
  return {
    daemonOffline: !snapshot.connected,
    partial: Boolean(snapshot.connected) && unavailable.length > 0,
    unavailable,
    offline,
    stale,
  };
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
  if (seconds <= 0) return "wygasło";
  const days = Math.floor(seconds / 86400); seconds %= 86400;
  const hours = Math.floor(seconds / 3600); seconds %= 3600;
  const minutes = Math.floor(seconds / 60); seconds %= 60;
  const clock = [hours, minutes, seconds].map((part) => String(part).padStart(2, "0")).join(":");
  return days ? `${days} ${plural(days, "dzień", "dni", "dni")} ${clock}` : clock;
}

function updateRetentionCountdowns() {
  document.querySelectorAll("[data-retain-until]").forEach((node) => {
    node.textContent = retentionCountdown(node.dataset.retainUntil);
  });
}

const repoIcons = {
  lock: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10" width="14" height="10" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>',
  unlock: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10" width="14" height="10" rx="2"/><path d="M9 10V7a4 4 0 0 1 7.5-2"/></svg>',
  publish: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 11v2a2 2 0 0 0 2 2h2l4 4V5L7 9H5a2 2 0 0 0-2 2Z"/><path d="M15 8a5 5 0 0 1 0 8M18 5a9 9 0 0 1 0 14"/></svg>',
  recovery: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v12M7 10l5 5 5-5M5 20h14"/></svg>',
  quarantine: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3 20 6v5c0 5-3.4 8.3-8 10-4.6-1.7-8-5-8-10V6l8-3Z"/><path d="M9 9l6 6M15 9l-6 6"/></svg>',
  pin: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 3h6l-1 5 3 3v2H7v-2l3-3-1-5M12 13v8"/></svg>',
  settings: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.08A1.7 1.7 0 0 0 8.97 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.52-1H3v-4h.08A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 8.97 4.6 1.7 1.7 0 0 0 10 3.08V3h4v.08A1.7 1.7 0 0 0 15.03 4.6a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9a1.7 1.7 0 0 0 1.52 1H21v4h-.08A1.7 1.7 0 0 0 19.4 15Z"/></svg>',
};

function repoAction(action, label, icon, extraClass = "") {
  return `<button class="repo-icon-action hint-button ${extraClass}" type="button" data-action="${escapeHTML(action)}" data-hint="${escapeHTML(label)}" aria-label="${escapeHTML(label)}">${icon}</button>`;
}

function renderRepo(repo) {
  const state = repo.display_state || "unknown";
  const deleted = Boolean(repo.server_deleted);
  const pending = deleted
    ? (repo.recovery_pending && repo.local_cleanup_pending ? "archiwum i czyszczenie czekają" : repo.recovery_pending ? "wydanie archiwum czeka" : repo.local_cleanup_pending ? "czyszczenie lokalne czeka" : "folder odłączony")
    : (repo.pending_files ? `${repo.pending_files} · ${bytes(repo.pending_bytes)}` : "brak zmian");
  const source = repo.local_path || (repo.attached ? "Folder FileES" : "Folder zdalny");
  const actions = [
    !deleted && !repo.attached ? repoAction("attach_repository", "Połącz z lokalnym folderem", repoIcons.pin, "attach") : "",
    repo.recovery_available ? repoAction("download_recovery", "Pobierz archiwum", repoIcons.recovery, "recovery") : "",
    repo.can_review_quarantine ? repoAction("review_quarantine", "Przejrzyj kwarantannę", repoIcons.quarantine, "quarantine") : "",
    repo.can_lock ? repoAction("lock", "Zablokuj pliki", repoIcons.lock, "mutate") : "",
    repo.can_unlock ? repoAction("unlock", "Zwolnij blokady", repoIcons.unlock, "mutate") : "",
    repo.can_publish ? repoAction("publish", "Opublikuj zmiany", repoIcons.publish, "publish") : "",
  ].join("");
  const stateLabel = stateLabels[state] || state;
  const disconnected = repo.connectivity !== "online" || ["offline", "unattached", "disabled", "revoked", "unknown"].includes(state);
  const stateOverlay = disconnected
    ? '<span class="repo-state-overlay" aria-hidden="true"><svg viewBox="0 0 16 16"><path d="M3 3l10 10M5.2 10.8 3.8 12.2a2 2 0 0 1-2.8-2.8l2.1-2.1M10.8 5.2l1.4-1.4A2 2 0 0 1 15 6.6l-2.1 2.1"/></svg></span>'
    : state === "attention"
      ? '<span class="repo-state-overlay is-attention" aria-hidden="true">!</span>'
      : state === "busy"
        ? '<span class="repo-state-overlay is-busy" aria-hidden="true"></span>'
        : "";
  const iconContents = `<span class="repo-icon" aria-hidden="true">${stateOverlay}</span>`;
  const open = repo.can_open
    ? `<button class="repo-open hint-button state-${escapeHTML(state)}" type="button" data-action="open_folder" data-hint="Otwórz folder · ${escapeHTML(stateLabel)}" aria-label="Otwórz folder · ${escapeHTML(stateLabel)}">${iconContents}</button>`
    : `<span class="repo-open is-disabled state-${escapeHTML(state)}" title="${escapeHTML(stateLabel)}" aria-label="${escapeHTML(stateLabel)}">${iconContents}</span>`;
  const settings = deleted ? "" : repoAction("settings", "Ustawienia folderu", repoIcons.settings, "repo-settings");
  const size = repo.attached && repo.working_copy_size_known && Number.isFinite(Number(repo.working_copy_bytes ?? 0))
    ? bytes(repo.working_copy_bytes ?? 0)
    : "—";
  return `<article class="repo-row" data-repo-id="${escapeHTML(repo.id)}">
    <div class="repo-title">
      ${open}
      <div class="repo-name"><strong title="${escapeHTML(repo.display_name)}">${escapeHTML(repo.display_name || repo.id)}</strong><small title="${escapeHTML(source)}">${escapeHTML(source)}</small></div>
    </div>
    <div class="repo-meta repo-queue"><small>${deleted ? "Stan lokalny" : "Kolejka"}</small><span title="${escapeHTML(deleted ? repo.cleanup_error : "")}">${escapeHTML(pending)}</span></div>
    <div class="repo-tools">${settings}${actions}</div>
    <div class="repo-meta repo-size"><small>Rozmiar</small><span>${escapeHTML(size)}</span></div>
  </article>`;
}

function renderRepoGroup(label, repos, className = "") {
  if (!repos.length) return "";
  return `<section class="realm-group ${escapeHTML(className)}">
    <div class="realm-divider"><span>${escapeHTML(label)}</span><b>${repos.length}</b></div>
    <div class="repo-list">${repos.map(renderRepo).join("")}</div>
  </section>`;
}

function renderRepositories(snapshot) {
  const root = $("#repositories");
  const repos = snapshot.repositories || [];
  const servers = [...(snapshot.servers || [])];
  if (!servers.length && !repos.length) {
    return replaceHTMLIfChanged(root, '<div class="empty-state"><span>◌</span><p>Nie ma jeszcze folderów do pokazania.</p></div>');
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
    const shelves = live.filter(isShelf);
    const trash = live.filter(isTrash);
    const rest = live.filter((repo) => !isShelf(repo) && !isTrash(repo));
    const attached = rest.filter((repo) => repo.attached);
    const remote = rest.filter((repo) => !repo.attached);
    const owned = attached.filter((repo) => repo.ownership === "owned");
    const guest = attached.filter((repo) => repo.ownership === "guest");
    const unclassified = attached.filter((repo) => !["owned", "guest"].includes(repo.ownership));
    const context = server.realm_alias || server.address || server.id;
    const expanded = expandedServers.has(server.id);
    const attention = serverRepos.some((repo) => repo.display_state === "attention" || Number(repo.conflicts || 0) > 0)
      || (snapshot.errors || []).some((error) => serverRepos.some((repo) => repo.id === error.repo_id))
      || (snapshot.notices || []).some((notice) => !notice.acked && serverRepos.some((repo) => repo.id === notice.repo_id));
    return `<article class="server-panel ${attention ? "has-attention" : ""}" data-server-id="${escapeHTML(server.id)}">
      <header class="server-header" data-toggle-server="${escapeHTML(server.id)}" tabindex="0" role="button" aria-expanded="${expanded}" aria-controls="server-folders-${escapeHTML(server.id)}">
        <div class="server-identity"><span class="server-mark" aria-hidden="true"></span><div>
          <div class="server-title-line">
            <h3>${escapeHTML(server.display_name || server.id)}</h3>
            <button class="server-settings" type="button" data-action="settings" title="Ustawienia serwera" aria-label="Ustawienia serwera ${escapeHTML(server.display_name || server.id)}">
              <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.08A1.7 1.7 0 0 0 8.97 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.52-1H3v-4h.08A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 8.97 4.6 1.7 1.7 0 0 0 10 3.08V3h4v.08A1.7 1.7 0 0 0 15.03 4.6a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9a1.7 1.7 0 0 0 1.52 1H21v4h-.08A1.7 1.7 0 0 0 19.4 15Z"></path></svg>
            </button>
          </div>
          <p title="${escapeHTML(context)}">${escapeHTML(context)}</p>
        </div></div>
        <div class="server-summary"><span class="server-total">${serverRepos.length} ${plural(serverRepos.length, "folder", "foldery", "folderów")}</span><span class="server-chevron" aria-hidden="true">⌄</span></div>
      </header>
      <div id="server-folders-${escapeHTML(server.id)}" class="server-folders" ${expanded ? "" : "hidden"}>
        ${serverRepos.length ? `<div class="repo-columns" aria-hidden="true"><span>Folder</span><span class="column-queue">Kolejka</span><span>Akcje</span><span>Rozmiar</span></div>
          ${renderRepoGroup("Własne", owned, "owned")}
          ${renderRepoGroup("Gościnne · udostępnione przez inne zespoły", guest, "guest")}
          ${renderRepoGroup("Półki przyjęcia", shelves, "upload-shelf")}
          ${renderRepoGroup("Kwarantanna", trash, "upload-trash")}
          ${renderRepoGroup("Pozostałe", unclassified, "unclassified")}
          ${renderRepoGroup("Usunięte · archiwa", deleted, "deleted")}
          ${renderRepoGroup("Zdalne", remote, "remote")}` : '<p class="server-empty">Ten serwer nie udostępnia jeszcze żadnego folderu.</p>'}
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
      ? "Oczekiwanie na połączenie"
      : action.phase === "awaiting_projection"
      ? "Potwierdzanie aktualnego stanu"
      : "Wykonywanie działania";
    return `<article class="action-badge">
      <span class="action-spinner" aria-hidden="true"></span>
      <div><strong>${escapeHTML(action.label || "Działanie FileES")}</strong><small>${escapeHTML(scope)} · ${escapeHTML(detail)}</small></div>
    </article>`;
  }).join("");
  replaceHTMLIfChanged(root, html);
}

function renderReservations(snapshot) {
  const card = $("#reservations-card");
  const root = $("#reservations");
  const reservations = snapshot.reservations || [];
	const holderRequests = (snapshot.lock_release_requests || []).filter((request) => request.role === "holder" && request.state === "pending");
  const reservationState = reservationProjectionState(snapshot);
	card.hidden = reservations.length === 0 && holderRequests.length === 0 && !reservationState.partial && !reservationState.daemonOffline && reservationState.offline.length === 0 && reservationState.stale.length === 0;
  $("#reservations-count").textContent = reservationState.partial ? `${reservations.length}+?` : reservations.length;
	const availabilityHTML = reservationState.partial
		? `<p class="muted">Częściowa lista — brak aktualnej emisji: ${reservationState.unavailable.map((server) => escapeHTML(server.display_name || server.id || "serwer")).join(", ")}.</p>`
		: reservationState.daemonOffline && (reservations.length || holderRequests.length)
		? '<p class="muted">Demon jest offline — pokazano ostatni znany stan.</p>'
		: reservationState.daemonOffline
		? '<p class="muted">Demon jest offline — projekcja blokad jest niezweryfikowana.</p>'
		: reservationState.offline.length
		? `<p class="muted">Lokalne lustro — tor stanowy offline: ${reservationState.offline.map((server) => escapeHTML(server.display_name || server.id || "serwer")).join(", ")}.</p>`
		: reservationState.stale.length
		? `<p class="muted">Serwer zwrócił wcześniejszą emisję: ${reservationState.stale.map((server) => escapeHTML(server.display_name || server.id || "serwer")).join(", ")}.</p>`
		: "";
	if (!reservations.length && !holderRequests.length) {
    replaceHTMLIfChanged(root, availabilityHTML);
    return;
  }
	const requestsHTML = holderRequests.map((request) => `<article class="reservation-row lock-release-request" data-lock-release-request-id="${escapeHTML(request.id)}">
		<div class="reservation-main">
			<strong title="${escapeHTML(request.path)}">Prośba o ${escapeHTML(request.path || "plik")}</strong>
			<p>${escapeHTML(request.counterparty_realm_alias || "Inna osoba")} · ${escapeHTML(request.repository || request.repo_id)}</p>
			<div class="lock-flags"><span class="lock-flag request">prośba o zwolnienie</span><span>${escapeHTML(shortDateTime(request.created_at))}</span></div>
		</div>
		<div class="reservation-request-actions">
			${request.can_dismiss ? '<button class="reservation-action secondary" data-action="dismiss_lock_release">OK</button>' : ""}
			${request.can_accept ? '<button class="reservation-action" data-action="accept_lock_release">Zwolnij</button>' : ""}
		</div>
	</article>`).join("");
	const reservationsHTML = reservations.map((reservation) => {
    const flags = [
      reservation.active_passport ? '<span class="lock-flag passport">paszport</span>' : "",
      reservation.local_changes ? '<span class="lock-flag risk">zmiany lokalne</span>' : "",
    ].join("");
		let action = '<span class="lock-owner">cudza</span>';
		if (reservation.can_release) action = '<button class="reservation-action" data-action="release_reservation">Zwolnij</button>';
		else if (reservation.can_request_release) action = '<button class="reservation-action" data-action="request_lock_release">Poproś o zwolnienie</button>';
		else if (reservation.lock_release_state === "pending") action = '<span class="lock-owner waiting">prośba wysłana</span>';
		else if (reservation.lock_release_state === "dismissed") action = '<span class="lock-owner">pozostawiono</span>';
		else if (reservation.lock_release_state === "accepted") action = '<span class="lock-owner waiting">zwalnianie…</span>';
    return `<article class="reservation-row" data-reservation-id="${escapeHTML(reservation.id)}">
      <div class="reservation-main">
        <strong title="${escapeHTML(reservation.path)}">${escapeHTML(reservation.path || "plik")}</strong>
        <p>${escapeHTML(reservation.repository || reservation.repo_id)} · ${escapeHTML(reservation.owner_label || "właściciel nieustawiony")}</p>
        <div class="lock-flags">${flags}<span>${escapeHTML(shortDateTime(reservation.created_at))}</span></div>
      </div>
      ${action}
    </article>`;
	}).join("");
	replaceHTMLIfChanged(root, availabilityHTML + requestsHTML + reservationsHTML);
}

function publicShareState(state) {
  if (state === "active") return "Aktywne";
  if (state === "revoked") return "Cofnięte";
  if (state === "deleted") return "Usunięte";
  return state || "Nieznane";
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
    ? `<div class="dashboard-share-bulk"><span>${selectedPublicShares.size} ${plural(selectedPublicShares.size, "wybrane", "wybrane", "wybranych")}</span><button type="button" data-share-bulk>Cofnij zaznaczone</button></div>`
    : "";
  let previousServer = "";
  const rows = shares.map((share) => {
    const serverChannels = activeByServer.get(share.server_id) || [];
    const serverHeader = share.server_id !== previousServer
      ? `<div class="dashboard-share-group" data-server-id="${escapeHTML(share.server_id)}"><span>${escapeHTML(servers.get(share.server_id) || share.server_id || "Serwer FileES")}</span>${serverChannels.length ? `<button type="button" data-share-revoke-all data-channel-ids="${escapeHTML(serverChannels.join(","))}">Cofnij aktywne</button>` : ""}</div>`
      : "";
    previousServer = share.server_id;
    const activeShare = share.state === "active";
    const scope = share.follow_head ? "śledzi HEAD" : "zamrożone";
    const audience = share.recipient_count
      ? `${share.recipient_count} ${plural(share.recipient_count, "odbiorca", "odbiorców", "odbiorców")}`
      : "kanał otwarty";
    const objects = `${share.object_count} ${plural(share.object_count, "plik", "pliki", "plików")}`;
    const manage = share.can_open
      ? `<button class="dashboard-share-open" type="button" data-action="manage_public_shares" aria-label="Otwórz udostępnienie ${escapeHTML(share.address)}">
          <span class="dashboard-share-dot ${activeShare ? "active" : ""}" aria-hidden="true"></span>
          <span class="dashboard-share-copy"><strong>${escapeHTML(share.address || "Udostępnienie")}</strong><small>${escapeHTML(share.repository)} · ${escapeHTML(objects)} · ${escapeHTML(scope)}</small><time>${escapeHTML(shortDateTime(share.updated_at))}</time></span>
        </button>`
      : `<div class="dashboard-share-open is-disabled"><span class="dashboard-share-dot ${activeShare ? "active" : ""}" aria-hidden="true"></span><span class="dashboard-share-copy"><strong>${escapeHTML(share.address || "Udostępnienie")}</strong><small>${escapeHTML(share.repository)} · ${escapeHTML(objects)} · ${escapeHTML(scope)}</small><time>${escapeHTML(shortDateTime(share.updated_at))}</time></span></div>`;
    const revoke = share.can_revoke ? '<button class="dashboard-share-revoke" type="button" data-action="revoke_public_share">Cofnij</button>' : "";
    return `${serverHeader}<article class="dashboard-share-row ${activeShare ? "is-active" : "is-inactive"}" data-server-id="${escapeHTML(share.server_id)}" data-repo-id="${escapeHTML(share.repo_id)}" data-channel-id="${escapeHTML(share.channel_id)}">
      <label class="dashboard-share-select" title="Dodaj do operacji zbiorczej">${share.can_revoke ? `<input type="checkbox" data-share-select ${selectedPublicShares.has(share.channel_id) ? "checked" : ""}><span aria-hidden="true"></span>` : ""}</label>${manage}<div class="dashboard-share-policy"><span>${escapeHTML(publicShareState(share.state))} · ${escapeHTML(audience)}</span><small>bezterminowo · wizyta 12 h</small></div>${revoke}
    </article>`;
  }).join("");
  replaceHTMLIfChanged(root, bulk + rows);
}

function renderShouts(snapshot) {
  const card = $("#shouts-card");
  const root = $("#shouts");
  const notices = snapshot.notices || [];
  const unread = notices.filter((notice) => !notice.acked).length;
  card.hidden = notices.length === 0;
  card.classList.toggle("has-unread", unread > 0);
  $("#shouts-count").textContent = unread
    ? `${unread} ${plural(unread, "ogłoszenie", "ogłoszenia", "ogłoszeń")} do przejrzenia`
    : "przeczytane";
  if (!notices.length) {
    replaceHTMLIfChanged(root, "");
    renderAnnouncementDialog(snapshot);
    return;
  }
  const repositories = new Map((snapshot.repositories || []).map((repo) => [repo.id, repo.display_name || repo.id]));
  const html = notices.slice(0, 5).map((notice) => {
    const repository = repositories.get(notice.repo_id) || notice.repo_id || "FileES";
    const revision = Number(notice.revision || 0) > 0 ? ` · r${Number(notice.revision)}` : "";
    const state = notice.acked ? "Przeczytane" : "Do przejrzenia";
    return `<button class="shout-row ${notice.acked ? "is-read" : "is-unread"}" type="button" data-notice-id="${escapeHTML(notice.id)}" aria-label="Otwórz ogłoszenie: ${escapeHTML(notice.title)}">
      <span class="shout-symbol" aria-hidden="true">${repoIcons.publish}</span>
      <span class="shout-main"><strong>${escapeHTML(notice.title || "Ogłoszenie")}</strong>
      <span>${escapeHTML(repository + revision)}</span><time>odebrano ${escapeHTML(shortDateTime(notice.created_at))}</time></span>
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

function openNewestUnreadAnnouncement() {
  const notices = currentSnapshot?.notices || [];
  const notice = notices.find((item) => !item.acked) || notices[0];
  if (notice) openAnnouncement(notice.id);
}

function renderAnnouncementDialog(snapshot) {
  if (!selectedAnnouncementID) return;
  const notice = (snapshot.notices || []).find((item) => item.id === selectedAnnouncementID);
  if (!notice || (announcementAckPending === notice.id && notice.acked)) {
    closeAnnouncement();
    return;
  }
  $("#announcement-copy").textContent = notice.title || "Ogłoszenie";
  $("#announcement-repository").textContent = announcementScope(snapshot, notice);
  const revision = $("#announcement-revision");
  revision.hidden = !(Number(notice.revision || 0) > 0);
  revision.textContent = revision.hidden ? "" : `rewizja r${Number(notice.revision)}`;
  $("#announcement-time").textContent = `odebrano ${shortDateTime(notice.created_at)}`;
  $("#announcement-status").textContent = notice.acked ? "Odczyt potwierdzony" : "Ogłoszenie wymaga potwierdzenia odczytu";
  $(".announcement-dialog .eyebrow").textContent = notice.acked ? "Przeczytane" : "Wymaga uwagi";
  const ack = $("#ack-announcement");
  ack.hidden = !notice.can_ack;
  ack.disabled = announcementAckPending === notice.id;
  ack.textContent = ack.disabled ? "Potwierdzanie…" : "Potwierdź odczyt";
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
      showToast({ level: "normal", title: "Nie można potwierdzić odczytu", message: actionErrors[result.code] || result.code });
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
    showToast({ level: "critical", title: "Nie udało się potwierdzić odczytu", message: error?.message || String(error) });
  }
}

function renderJournal(snapshot) {
  const entries = snapshot.journal || [];
  const root = $("#activity");
  if (!entries.length) {
    replaceHTMLIfChanged(root, '<p class="muted">Brak nowych sygnałów.</p>');
  } else {
    replaceHTMLIfChanged(root, entries.slice(0, 6).map((item) => `<article class="activity-row ${item.emphasized ? "is-error" : ""}">
      <span class="activity-dot"></span><div><strong title="${escapeHTML(item.summary)}">${escapeHTML(item.summary)}</strong>
      <p>${escapeHTML(item.repository || "FileES")}</p><time datetime="${escapeHTML(item.exact_time)}">${escapeHTML(item.relative_time)}</time></div>
    </article>`).join(""));
  }

  const full = $("#journal");
  replaceHTMLIfChanged(full, entries.length ? entries.map((item) => `<article class="journal-row ${item.emphasized ? "is-error" : ""}">
    <time>${escapeHTML(item.exact_time)}</time>
    <span class="journal-repo">${escapeHTML(item.repository || "FileES")}</span>
    <div class="journal-copy"><strong>${escapeHTML(item.summary)}</strong>${item.details ? `<p>${escapeHTML(item.details)}</p>` : ""}</div>
  </article>`).join("") : '<p class="muted">Brak wpisów.</p>');
}

function render(snapshot) {
  if (!snapshot) return;
  currentSnapshot = snapshot;
  const clientVersion = String(snapshot.client_version || "").trim();
  const versionBadge = $("#client-version");
  versionBadge.textContent = clientVersion || "—";
  versionBadge.title = clientVersion ? `Wersja klienta FileES ${clientVersion}` : "Wersja klienta FileES jest nieznana";
  renderVersionDialog(snapshot);
  const pairButton = $("#pair-mobile");
  const capabilities = new Set(snapshot.capabilities || []);
  pairButton.disabled = !snapshot.connected || snapshot.stale || !(snapshot.servers || []).length || !capabilities.has("mobile_pairing.begin");
  renderConnection(snapshot);
  renderMetrics(snapshot);
  const repositoriesChanged = renderRepositories(snapshot);
  renderActions(snapshot);
  renderReservations(snapshot);
  renderPublicShares(snapshot);
  renderShouts(snapshot);
	renderUpdate(snapshot);
  renderJournal(snapshot);
  $("#last-refresh").textContent = dateTime(snapshot.last_refresh);
  $("#revision").textContent = `stan #${snapshot.revision || 0}`;
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
  $("#version-client").textContent = clientVersion || "nieznana";
  $("#version-channel").textContent = channel || "nieustalony";
  $("#version-release").textContent = currentRelease || "nieustalone";
  if (!update) {
    $("#version-status").textContent = "Demon nie udostępnił jeszcze informacji o kanale aktualizacji.";
  } else if (available) {
    $("#version-status").textContent = update.summary || `Dostępne jest wydanie ${availableRelease}. Zainstalowane wydanie: ${currentRelease || "nieustalone"}.`;
  } else {
    $("#version-status").textContent = update.summary || "Masz aktualne wydanie z wybranego kanału aktualizacji.";
  }
  $("#version-update-actions").hidden = !available;
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
	const available = Boolean(update?.available_version) && update.state !== "current";
	card.hidden = !available;
	if (!available) return;
	$("#update-version").textContent = update.available_version;
	$("#update-summary").textContent = update.summary || `Zainstalowana wersja: ${update.current_version || "nieznana"}.`;
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
    });
    if (!result.accepted) {
      showToast({ level: "normal", title: "Akcja niedostępna", message: actionErrors[result.code] || result.code });
    }
  } catch (error) {
    showToast({ level: "critical", title: "Nie udało się przekazać intencji", message: error?.message || String(error) });
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
      showToast({ level: "normal", title: "Akcja niedostępna", message: actionErrors[result.code] || result.code });
      return;
    }
    selectedPublicShares.clear();
    selectedPublicShareServer = "";
    renderPublicShares(currentSnapshot);
  } catch (error) {
    showToast({ level: "critical", title: "Nie udało się przekazać intencji", message: error?.message || String(error) });
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
Events.On("filees:action-feedback", (event) => showToast(event?.data ?? event));
Events.On("filees:open-announcement", openNewestUnreadAnnouncement);
$("#activate").addEventListener("click", (event) => triggerAction(event.currentTarget));
$("#pair-mobile").addEventListener("click", (event) => triggerAction(event.currentTarget));
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
  const action = expanded ? "Rozwiń" : "Zwiń";
  const subject = toggle.getAttribute("aria-label")?.replace(/^(Zwiń|Rozwiń)\s+/u, "") || "panel";
  toggle.title = `${action} ${subject}`;
  toggle.setAttribute("aria-label", `${action} ${subject}`);
});
$("#open-journal").addEventListener("click", () => {
  $("#journal-overlay").hidden = false;
  $("#close-journal").focus();
});
$("#close-journal").addEventListener("click", () => { $("#journal-overlay").hidden = true; });
$("#journal-overlay").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) event.currentTarget.hidden = true;
});
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
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
  const toggle = event.target.closest("[data-toggle-server]");
  if (!toggle || !["Enter", " "].includes(event.key)) return;
  event.preventDefault();
  const serverID = toggle.dataset.toggleServer;
  if (expandedServers.has(serverID)) expandedServers.delete(serverID);
  else expandedServers.add(serverID);
  if (renderRepositories(currentSnapshot)) scheduleWindowFit();
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
