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
    ? `Demon publikuje spójny obraz ${repos.length} repozytoriów. Ten ekran tylko go renderuje i przekazuje intencje przez IPC.`
    : "Brak połączenia z demonem. Renderer zachowuje ostatnią projekcję i nie próbuje odtwarzać prawdy z dysku.";
}

function renderRepositories(snapshot) {
  const root = $("#repositories");
  const repos = snapshot.repositories || [];
  if (!repos.length) {
    root.innerHTML = '<div class="empty-state"><span>◌</span><p>W projekcji nie ma jeszcze repozytoriów.</p></div>';
    return;
  }
  root.innerHTML = repos.map((repo) => {
    const state = repo.display_state || "unknown";
    const revision = repo.attached ? `r${repo.local_revision || 0} / r${repo.head_revision || 0}` : "projekcja zdalna";
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
  }).join("");
}

function renderServers(snapshot) {
  const root = $("#servers");
  const servers = snapshot.servers || [];
  if (!servers.length) {
    root.innerHTML = '<p class="muted">Brak aktywnych serwerów.</p>';
    return;
  }
  root.innerHTML = servers.map((server) => `<article class="server-row">
    <header><strong>${escapeHTML(server.display_name || server.id)}</strong><span class="server-count">${Number(server.repository_count || 0)} repo</span></header>
    <p title="${escapeHTML(server.address)}">${escapeHTML(server.realm_alias || server.address || server.id)}</p>
  </article>`).join("");
}

function renderActivity(snapshot) {
  const root = $("#activity");
  const errors = (snapshot.errors || []).slice(0, 3).map((item) => ({
    title: item.message || item.code, detail: item.hint || item.repo_id || "Komunikat demona", error: true,
  }));
  const activity = (snapshot.activity || []).slice(0, Math.max(0, 6 - errors.length)).map((item) => ({
    title: item.path || item.repo_id, detail: `${item.kind || "zmiana"} · ${item.stage || "wykryto"}`, error: Boolean(item.error_id),
  }));
  const items = [...errors, ...activity];
  if (!items.length) {
    root.innerHTML = '<p class="muted">Brak nowych sygnałów.</p>';
    return;
  }
  root.innerHTML = items.map((item) => `<article class="activity-row ${item.error ? "is-error" : ""}">
    <span class="activity-dot"></span><div><strong>${escapeHTML(item.title)}</strong><p>${escapeHTML(item.detail)}</p></div>
  </article>`).join("");
}

function render(snapshot) {
  if (!snapshot) return;
  renderConnection(snapshot);
  renderMetrics(snapshot);
  renderRepositories(snapshot);
  renderServers(snapshot);
  renderActivity(snapshot);
  $("#last-refresh").textContent = dateTime(snapshot.last_refresh);
  $("#revision").textContent = `projekcja #${snapshot.revision || 0}`;
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
  const row = button.closest("[data-repo-id]");
  if (!row) return;
  button.disabled = true;
  try {
    const result = await GUIService.Trigger({ kind: button.dataset.action, repo_id: row.dataset.repoId });
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
$("#repositories").addEventListener("click", (event) => {
  const button = event.target.closest("[data-action]");
  if (button) triggerAction(button);
});
$("#window-minimise").addEventListener("click", () => Window.Minimise());
$("#window-maximise").addEventListener("click", () => Window.ToggleMaximise());
$("#window-close").addEventListener("click", () => Window.Close());
$("#titlebar").addEventListener("dblclick", (event) => {
  if (!event.target.closest(".topbar-actions")) Window.ToggleMaximise();
});

try {
  render(await GUIService.Snapshot());
} catch (error) {
  console.error("Nie udało się pobrać projekcji FileES", error);
}
