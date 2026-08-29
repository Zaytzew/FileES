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
const renderedHTML = new WeakMap();
const expandedServers = new Set();
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

function renderConnection(snapshot) {
  const core = $("#pulse-core");
  core.className = "pulse-core";
  let connectionLabel = "Demon jest rozłączony";
  if (snapshot.connected && !snapshot.stale) {
    core.classList.add("is-online");
    connectionLabel = "Połączenie z demonem jest aktywne";
  } else if (snapshot.connected) {
    core.classList.add("is-stale");
    connectionLabel = "Demon odświeża projekcję";
  } else {
    core.classList.add("is-offline");
  }
  $("#offline").hidden = Boolean(snapshot.connected);
  $("#pulse-card").dataset.connection = snapshot.connected && !snapshot.stale ? "online" : snapshot.connected ? "stale" : "offline";
  $("#pulse-card").dataset.connectionLabel = connectionLabel;
}

function renderMetrics(snapshot) {
  const repos = snapshot.repositories || [];
  const pending = repos.reduce((sum, repo) => sum + Number(repo.pending_files || 0), 0);
  const pendingBytes = repos.reduce((sum, repo) => sum + Number(repo.pending_bytes || 0), 0);
  const conflicts = repos.reduce((sum, repo) => sum + Number(repo.conflicts || 0), 0);
  const unreadAnnouncements = (snapshot.notices || []).filter((notice) => !notice.acked).length;
  const attention = conflicts + unreadAnnouncements + (snapshot.errors?.length || 0);

  $("#metric-servers").textContent = snapshot.servers?.length ?? 0;
  $("#metric-repos").textContent = repos.length;
  $("#metric-pending").textContent = pending;
  $("#metric-pending-note").textContent = pending ? `${bytes(pendingBytes)} oczekuje` : "kolejka jest pusta";
  $("#metric-attention").textContent = attention;
  $("#pulse-value").textContent = repos.length;
  $("#pulse-label").textContent = plural(repos.length, "repozytorium", "repozytoria", "repozytoriów");
  $("#pulse-card").classList.toggle("has-attention", attention > 0);
  const connectionLabel = $("#pulse-card").dataset.connectionLabel || "Stan połączenia nieznany";
  $("#pulse-card").title = attention > 0
    ? `${connectionLabel}. ${attention} ${plural(attention, "uwaga", "uwagi", "uwag")} do sprawdzenia.`
    : `${connectionLabel}. Repozytoria nie wymagają uwagi.`;
  $("#hero-copy").textContent = snapshot.connected
    ? "Zmiany i działania pojawiają się tutaj na bieżąco."
    : "Połączenie jest chwilowo niedostępne. Panel zachowuje ostatni znany stan i odświeży się automatycznie.";
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
  const inventoryUnknown = Boolean(snapshot.connected) && (snapshot.servers || []).some((server) => !server.reservations_known);
  card.hidden = reservations.length === 0 && !inventoryUnknown;
  $("#reservations-count").textContent = inventoryUnknown ? "?" : reservations.length;
  if (!reservations.length) {
    replaceHTMLIfChanged(root, inventoryUnknown ? '<p class="muted">Lista blokad jest chwilowo niedostępna.</p>' : "");
    return;
  }
  const html = reservations.map((reservation) => {
    const flags = [
      reservation.active_passport ? '<span class="lock-flag passport">paszport</span>' : "",
      reservation.local_changes ? '<span class="lock-flag risk">zmiany lokalne</span>' : "",
    ].join("");
    const action = reservation.can_release
      ? '<button class="reservation-action" data-action="release_reservation">Zwolnij</button>'
      : '<span class="lock-owner">cudza</span>';
    return `<article class="reservation-row" data-reservation-id="${escapeHTML(reservation.id)}">
      <div class="reservation-main">
        <strong title="${escapeHTML(reservation.path)}">${escapeHTML(reservation.path || "plik")}</strong>
        <p>${escapeHTML(reservation.repository || reservation.repo_id)} · ${escapeHTML(reservation.owner_label || "właściciel nieustawiony")}</p>
        <div class="lock-flags">${flags}<span>${escapeHTML(shortDateTime(reservation.created_at))}</span></div>
      </div>
      ${action}
    </article>`;
  }).join("");
  replaceHTMLIfChanged(root, html);
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
  const pairButton = $("#pair-mobile");
  const capabilities = new Set(snapshot.capabilities || []);
  pairButton.disabled = !snapshot.connected || snapshot.stale || !(snapshot.servers || []).length || !capabilities.has("mobile_pairing.begin");
  renderConnection(snapshot);
  renderMetrics(snapshot);
  const repositoriesChanged = renderRepositories(snapshot);
  renderActions(snapshot);
  renderReservations(snapshot);
  renderShouts(snapshot);
	renderUpdate(snapshot);
  renderJournal(snapshot);
  $("#last-refresh").textContent = dateTime(snapshot.last_refresh);
  $("#revision").textContent = `stan #${snapshot.revision || 0}`;
  if (repositoriesChanged) scheduleWindowFit();
  updateRetentionCountdowns();
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
  const noticeRow = button.closest("[data-notice-id]");
  const serverPanel = button.closest("[data-server-id]");
	const globalAction = button.closest("[data-global-action]");
	if (!repoRow && !reservationRow && !noticeRow && !serverPanel && !globalAction) return;
  button.disabled = true;
  try {
    const result = await GUIService.Trigger({
      kind: button.dataset.action,
      repo_id: repoRow?.dataset.repoId || "",
      server_id: serverPanel?.dataset.serverId || "",
      reservation_id: reservationRow?.dataset.reservationId || "",
      notice_id: noticeRow?.dataset.noticeId || "",
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
