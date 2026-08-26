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
const renderedHTML = new WeakMap();
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

function prepareRepositoryWidths() {
  const rows = [...document.querySelectorAll(".repo-row")];
  // Every row in every server panel must use the same first-column width.
  // Per-row sizing made revision, queue, state and action cells visibly drift
  // as repository names changed between daemon ticks.
  const compactMinimum = window.innerWidth <= 800 ? 210 : window.innerWidth <= 900 ? 220 : 250;
  const naturalTitleWidth = rows.reduce((largest, row) => {
    const name = row.querySelector(".repo-name strong");
    return Math.max(largest, name ? Math.ceil(name.scrollWidth) + 58 : 0);
  }, compactMinimum);
  const titleWidth = Math.max(compactMinimum, Math.min(390, naturalTitleWidth));
  document.querySelectorAll(".server-folders").forEach((panel) => {
    panel.style.setProperty("--repo-title-min", `${titleWidth}px`);
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
  return date.toLocaleString("pl-PL", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
}

function renderConnection(snapshot) {
  const node = $("#connection");
  const label = node.querySelector(".connection-label");
  node.className = "connection";
  if (snapshot.connected && !snapshot.stale) {
    node.classList.add("is-online");
    label.textContent = "Połączono";
  } else if (snapshot.connected) {
    node.classList.add("is-stale");
    label.textContent = "Odświeżanie";
  } else {
    node.classList.add("is-offline");
    label.textContent = "Rozłączono";
  }
  $("#offline").hidden = Boolean(snapshot.connected);
  $("#pulse-core").classList.toggle("is-error", !snapshot.connected || snapshot.icon_state === "error");
}

function renderMetrics(snapshot) {
  const repos = snapshot.repositories || [];
  const pending = repos.reduce((sum, repo) => sum + Number(repo.pending_files || 0), 0);
  const pendingBytes = repos.reduce((sum, repo) => sum + Number(repo.pending_bytes || 0), 0);
  const conflicts = repos.reduce((sum, repo) => sum + Number(repo.conflicts || 0), 0);
  const attention = conflicts + (snapshot.notices?.length || 0) + (snapshot.errors?.length || 0);

  $("#metric-servers").textContent = snapshot.servers?.length ?? 0;
  $("#metric-repos").textContent = repos.length;
  $("#metric-pending").textContent = pending;
  $("#metric-pending-note").textContent = pending ? `${bytes(pendingBytes)} oczekuje` : "kolejka jest pusta";
  $("#metric-attention").textContent = attention;
  $("#pulse-value").textContent = repos.length;
  $("#hero-copy").textContent = snapshot.connected
    ? `FileES opiekuje się ${repos.length} ${plural(repos.length, "folderem", "folderami", "folderami")} na ${snapshot.servers?.length || 0} ${plural(snapshot.servers?.length || 0, "serwerze", "serwerach", "serwerach")}. Zmiany i działania pojawiają się tutaj na bieżąco.`
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

function renderRepo(repo) {
  const state = repo.display_state || "unknown";
  const deleted = Boolean(repo.server_deleted);
  const revision = deleted
    ? `<span data-retain-until="${escapeHTML(repo.retain_until)}"></span>`
    : escapeHTML(repo.attached ? `r${repo.local_revision || 0} / r${repo.head_revision || 0}` : "zdalny");
  const pending = deleted
    ? (repo.recovery_pending && repo.local_cleanup_pending ? "archiwum i czyszczenie czekają" : repo.recovery_pending ? "wydanie archiwum czeka" : repo.local_cleanup_pending ? "czyszczenie lokalne czeka" : "folder odłączony")
    : (repo.pending_files ? `${repo.pending_files} · ${bytes(repo.pending_bytes)}` : "brak zmian");
  // Transport URLs are daemon internals, not useful (or safe) UI labels.
  const source = repo.local_path || (repo.attached ? "Folder FileES" : "Folder zdalny");
  const actions = [
    repo.can_open ? '<button class="repo-action" data-action="open_folder" title="Otwórz lokalny folder">Otwórz</button>' : "",
    repo.recovery_available ? '<button class="repo-action mutate" data-action="download_recovery" title="Pobierz archiwum usuniętego repozytorium">Pobierz archiwum</button>' : "",
    repo.can_lock ? '<button class="repo-action mutate" data-action="lock" title="Wybierz i zablokuj pliki">Zablokuj</button>' : "",
    repo.can_unlock ? '<button class="repo-action mutate" data-action="unlock" title="Wybierz i zwolnij blokady">Zwolnij</button>' : "",
    deleted ? "" : `<button class="repo-settings" data-action="settings" title="Działania dla folderu" aria-label="Działania dla folderu ${escapeHTML(repo.display_name || repo.id)}">
      <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.08A1.7 1.7 0 0 0 8.97 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.52-1H3v-4h.08A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 8.97 4.6 1.7 1.7 0 0 0 10 3.08V3h4v.08A1.7 1.7 0 0 0 15.03 4.6a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9a1.7 1.7 0 0 0 1.52 1H21v4h-.08A1.7 1.7 0 0 0 19.4 15Z"></path></svg>
    </button>`,
  ].join("");
  return `<article class="repo-row" data-repo-id="${escapeHTML(repo.id)}">
    <div class="repo-title"><span class="repo-icon" aria-hidden="true"></span><div class="repo-name">
      <strong title="${escapeHTML(repo.display_name)}">${escapeHTML(repo.display_name || repo.id)}</strong>
      <small title="${escapeHTML(source)}">${escapeHTML(source)}</small>
    </div></div>
    <div class="repo-meta repo-revision"><small>${deleted ? "Czas na pobranie" : "Rewizja"}</small><span>${revision}</span></div>
    <div class="repo-meta repo-queue"><small>${deleted ? "Stan lokalny" : "Kolejka"}</small><span title="${escapeHTML(deleted ? repo.cleanup_error : "")}">${escapeHTML(pending)}</span></div>
    <span class="state-pill ${escapeHTML(state)}">${escapeHTML(stateLabels[state] || state)}</span>
    <div class="repo-actions">${actions}</div>
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
  if (!repos.length) {
    return replaceHTMLIfChanged(root, '<div class="empty-state"><span>◌</span><p>Nie ma jeszcze folderów do pokazania.</p></div>');
  }
  const servers = [...(snapshot.servers || [])];
  const known = new Set(servers.map((server) => server.id));
  repos.forEach((repo) => {
    if (!known.has(repo.server_id)) {
      servers.push({ id: repo.server_id, display_name: repo.server_id, address: "" });
      known.add(repo.server_id);
    }
  });
  const html = servers.map((server) => {
    const serverRepos = repos.filter((repo) => repo.server_id === server.id);
    if (!serverRepos.length) return "";
    const deleted = serverRepos.filter((repo) => repo.server_deleted);
    const activeRepos = serverRepos.filter((repo) => !repo.server_deleted);
    const owned = activeRepos.filter((repo) => repo.ownership === "owned");
    const guest = activeRepos.filter((repo) => repo.ownership === "guest");
    const unclassified = activeRepos.filter((repo) => !["owned", "guest"].includes(repo.ownership));
    const context = server.realm_alias || server.address || server.id;
    return `<article class="server-panel" data-server-id="${escapeHTML(server.id)}">
      <header class="server-header">
        <div class="server-identity"><span class="server-mark" aria-hidden="true"></span><div>
          <div class="server-title-line">
            <h3>${escapeHTML(server.display_name || server.id)}</h3>
            <button class="server-settings" type="button" data-action="settings" title="Ustawienia serwera" aria-label="Ustawienia serwera ${escapeHTML(server.display_name || server.id)}">
              <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.08A1.7 1.7 0 0 0 8.97 19.4a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-1.52-1H3v-4h.08A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 8.97 4.6 1.7 1.7 0 0 0 10 3.08V3h4v.08A1.7 1.7 0 0 0 15.03 4.6a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 9a1.7 1.7 0 0 0 1.52 1H21v4h-.08A1.7 1.7 0 0 0 19.4 15Z"></path></svg>
            </button>
          </div>
          <p title="${escapeHTML(context)}">${escapeHTML(context)}</p>
        </div></div>
        <span class="server-total">${serverRepos.length} ${plural(serverRepos.length, "folder", "foldery", "folderów")}</span>
      </header>
      <div class="server-folders">
        <div class="repo-columns" aria-hidden="true"><span>Folder</span><span class="column-revision">Rewizja</span><span class="column-queue">Kolejka</span><span>Stan</span><span>Akcje</span></div>
        ${renderRepoGroup("Własne", owned, "owned")}
        ${renderRepoGroup("Gościnne · udostępnione przez inne zespoły", guest, "guest")}
        ${renderRepoGroup("Pozostałe", unclassified, "unclassified")}
        ${renderRepoGroup("Usunięte · archiwa", deleted, "deleted")}
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
  renderConnection(snapshot);
  renderMetrics(snapshot);
  const repositoriesChanged = renderRepositories(snapshot);
  renderActions(snapshot);
  renderReservations(snapshot);
  renderJournal(snapshot);
  $("#last-refresh").textContent = dateTime(snapshot.last_refresh);
  $("#revision").textContent = `stan #${snapshot.revision || 0}`;
  if (repositoriesChanged) scheduleWindowFit();
  updateRetentionCountdowns();
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
  const serverPanel = button.closest("[data-server-id]");
  if (!repoRow && !reservationRow && !serverPanel) return;
  button.disabled = true;
  try {
    const result = await GUIService.Trigger({
      kind: button.dataset.action,
      repo_id: repoRow?.dataset.repoId || "",
      server_id: serverPanel?.dataset.serverId || "",
      reservation_id: reservationRow?.dataset.reservationId || "",
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
  if (event.key === "Escape" && !$("#journal-overlay").hidden) $("#journal-overlay").hidden = true;
});
$("#repositories").addEventListener("click", (event) => {
  const button = event.target.closest("[data-action]");
  if (button) triggerAction(button);
});
$("#reservations").addEventListener("click", (event) => {
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
