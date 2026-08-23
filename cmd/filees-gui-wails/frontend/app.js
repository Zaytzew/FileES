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
};

const actionErrors = {
  actions_unavailable: "Akcje systemowe są niedostępne w tym buildzie.",
  action_unavailable: "Ta akcja nie jest już dostępna. Stan repozytorium mógł się zmienić.",
  action_queue_busy: "Kolejka intencji GUI jest zajęta. Spróbuj ponownie.",
};

let currentSnapshot = null;
const initialWindowWidth = 1180;
const autoFit = {
  enabled: true,
  appliedWidth: initialWindowWidth,
  running: false,
  queued: false,
  suppressResizeUntil: 0,
};

function recordNumber(value, key) {
  return Number(value?.[key] ?? value?.[key.toLowerCase()] ?? 0);
}

function prepareRepositoryWidths() {
  const rows = [...document.querySelectorAll(".repo-row")];
  rows.forEach((row) => {
    const name = row.querySelector(".repo-name strong");
    if (!name) return;
    // The path may be arbitrarily long and remains intentionally elided.  The
    // human-facing name gets a useful (but bounded) natural-width allowance.
    const titleWidth = Math.max(190, Math.min(360, Math.ceil(name.scrollWidth) + 47));
    row.style.setProperty("--repo-title-min", `${titleWidth}px`);
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

function renderRepo(repo) {
  const state = repo.display_state || "unknown";
  const revision = repo.attached ? `r${repo.local_revision || 0} / r${repo.head_revision || 0}` : "zdalny";
  const pending = repo.pending_files ? `${repo.pending_files} · ${bytes(repo.pending_bytes)}` : "brak zmian";
  const source = repo.local_path || repo.url || repo.id;
  const actions = [
    repo.can_open ? '<button class="repo-action" data-action="open_folder" title="Otwórz lokalny folder">Otwórz</button>' : "",
    repo.can_lock ? '<button class="repo-action mutate" data-action="lock" title="Wybierz i zablokuj pliki">Zablokuj</button>' : "",
    repo.can_unlock ? '<button class="repo-action mutate" data-action="unlock" title="Wybierz i zwolnij blokady">Zwolnij</button>' : "",
  ].join("");
  return `<article class="repo-row" data-repo-id="${escapeHTML(repo.id)}">
    <div class="repo-title"><span class="repo-icon">▰</span><div class="repo-name">
      <strong title="${escapeHTML(repo.display_name)}">${escapeHTML(repo.display_name || repo.id)}</strong>
      <small title="${escapeHTML(source)}">${escapeHTML(source)}</small>
    </div></div>
    <div class="repo-meta"><small>Rewizja</small><span>${escapeHTML(revision)}</span></div>
    <div class="repo-meta"><small>Kolejka</small><span>${escapeHTML(pending)}</span></div>
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
    root.innerHTML = '<div class="empty-state"><span>◌</span><p>Nie ma jeszcze folderów do pokazania.</p></div>';
    return;
  }
  const servers = [...(snapshot.servers || [])];
  const known = new Set(servers.map((server) => server.id));
  repos.forEach((repo) => {
    if (!known.has(repo.server_id)) {
      servers.push({ id: repo.server_id, display_name: repo.server_id, address: "" });
      known.add(repo.server_id);
    }
  });
  root.innerHTML = servers.map((server) => {
    const serverRepos = repos.filter((repo) => repo.server_id === server.id);
    if (!serverRepos.length) return "";
    const owned = serverRepos.filter((repo) => repo.ownership === "owned");
    const guest = serverRepos.filter((repo) => repo.ownership === "guest");
    const unclassified = serverRepos.filter((repo) => !["owned", "guest"].includes(repo.ownership));
    const context = server.realm_alias || server.address || server.id;
    return `<article class="server-panel">
      <header class="server-header">
        <div class="server-identity"><span class="server-mark" aria-hidden="true"></span><div>
          <h3>${escapeHTML(server.display_name || server.id)}</h3>
          <p title="${escapeHTML(context)}">${escapeHTML(context)}</p>
        </div></div>
        <span class="server-total">${serverRepos.length} ${plural(serverRepos.length, "folder", "foldery", "folderów")}</span>
      </header>
      <div class="server-folders">
        ${renderRepoGroup("Własne", owned, "owned")}
        ${renderRepoGroup("Gościnne · udostępnione przez inne zespoły", guest, "guest")}
        ${renderRepoGroup("Pozostałe", unclassified, "unclassified")}
      </div>
    </article>`;
  }).join("");
}

function renderActions(snapshot) {
  const root = $("#action-status");
  const actions = snapshot.pending_actions || [];
  root.hidden = actions.length === 0;
  root.innerHTML = actions.map((action) => {
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
}

function updateCycleCountdown() {
  const node = $("#cycle-countdown");
  if (!currentSnapshot || !node) return;
  if (!currentSnapshot.connected) {
    node.textContent = "Harmonogram wstrzymany";
    node.classList.remove("is-running");
    return;
  }
  if (currentSnapshot.cycle_running) {
    node.textContent = "Sprawdzanie teraz";
    node.classList.add("is-running");
    return;
  }
  node.classList.remove("is-running");
  const next = new Date(currentSnapshot.next_cycle_at || "");
  if (Number.isNaN(next.getTime())) {
    node.textContent = "Harmonogram niedostępny";
    return;
  }
  const seconds = Math.max(0, Math.ceil((next.getTime() - Date.now()) / 1000));
  if (seconds < 60) {
    node.textContent = seconds === 0 ? "Kolejne sprawdzenie za chwilę" : `Kolejne sprawdzenie za ${seconds} s`;
    return;
  }
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  node.textContent = `Kolejne sprawdzenie za ${minutes}:${String(rest).padStart(2, "0")}`;
}

function renderReservations(snapshot) {
  const card = $("#reservations-card");
  const root = $("#reservations");
  const reservations = snapshot.reservations || [];
  const inventoryUnknown = Boolean(snapshot.connected) && (snapshot.servers || []).some((server) => !server.reservations_known);
  card.hidden = reservations.length === 0 && !inventoryUnknown;
  $("#reservations-count").textContent = inventoryUnknown ? "?" : reservations.length;
  if (!reservations.length) {
    root.innerHTML = inventoryUnknown ? '<p class="muted">Lista blokad jest chwilowo niedostępna.</p>' : "";
    return;
  }
  root.innerHTML = reservations.map((reservation) => {
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
}

function renderJournal(snapshot) {
  const entries = snapshot.journal || [];
  const root = $("#activity");
  if (!entries.length) {
    root.innerHTML = '<p class="muted">Brak nowych sygnałów.</p>';
  } else {
    root.innerHTML = entries.slice(0, 6).map((item) => `<article class="activity-row ${item.emphasized ? "is-error" : ""}">
      <span class="activity-dot"></span><div><strong title="${escapeHTML(item.summary)}">${escapeHTML(item.summary)}</strong>
      <p>${escapeHTML(item.repository || "FileES")}</p><time datetime="${escapeHTML(item.exact_time)}">${escapeHTML(item.relative_time)}</time></div>
    </article>`).join("");
  }

  const full = $("#journal");
  full.innerHTML = entries.length ? entries.map((item) => `<article class="journal-row ${item.emphasized ? "is-error" : ""}">
    <time>${escapeHTML(item.exact_time)}</time>
    <span class="journal-repo">${escapeHTML(item.repository || "FileES")}</span>
    <div class="journal-copy"><strong>${escapeHTML(item.summary)}</strong>${item.details ? `<p>${escapeHTML(item.details)}</p>` : ""}</div>
  </article>`).join("") : '<p class="muted">Brak wpisów.</p>';
}

function render(snapshot) {
  if (!snapshot) return;
  currentSnapshot = snapshot;
  renderConnection(snapshot);
  renderMetrics(snapshot);
  renderRepositories(snapshot);
  renderActions(snapshot);
  renderReservations(snapshot);
  renderJournal(snapshot);
  $("#last-refresh").textContent = dateTime(snapshot.last_refresh);
  $("#revision").textContent = `stan #${snapshot.revision || 0}`;
  updateCycleCountdown();
  scheduleWindowFit();
}

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
  if (!repoRow && !reservationRow) return;
  button.disabled = true;
  try {
    const result = await GUIService.Trigger({
      kind: button.dataset.action,
      repo_id: repoRow?.dataset.repoId || "",
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
      else autoFit.appliedWidth = Math.max(autoFit.appliedWidth, width);
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

window.setInterval(updateCycleCountdown, 1000);
