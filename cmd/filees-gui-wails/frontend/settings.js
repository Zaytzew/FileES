import { Events, Window } from "/wails/runtime.js";
import { SettingsService } from "./bindings/filees/cmd/filees-gui-wails/index.js";
import { initializeTheme } from "./theme-preference.js";
import { initializeLanguage, t, labelHTML } from "./i18n.js";

initializeTheme();
initializeLanguage();

const $ = (selector) => document.querySelector(selector);
const escapeHTML = (value) => String(value ?? "")
  .replaceAll("&", "&amp;")
  .replaceAll("<", "&lt;")
  .replaceAll(">", "&gt;")
  .replaceAll('"', "&quot;")
  .replaceAll("'", "&#039;");

let currentSnapshot = null;

// Locale updates must not rebuild buttons with an in-flight choice.
function refreshSettingsLabels() {
  if (!currentSnapshot) return;
  const server = currentSnapshot.server;
  $("#settings-copy").textContent = currentSnapshot.text || t("settings.copy");
  $("#realm-badge").textContent = server.realm || t("settings.noRealm");
  $("#server-address").textContent = server.address || t("settings.noData");
  $("#server-realm").textContent = server.realm || t("settings.noAlias");
  $("#server-client").textContent = server.client_id || t("settings.noData");
  $("#change-timeout").title = server.can_set_session_timeout ? t("settings.changeTimeout") : t("settings.unsupportedTimeout");
}

window.addEventListener("filees:language-changed", refreshSettingsLabels);

function showToast(title, message = "") {
  const toast = document.createElement("article");
  toast.className = "toast";
  toast.innerHTML = `<strong>${escapeHTML(title)}</strong>${message ? `<span>${escapeHTML(message)}</span>` : ""}`;
  $("#settings-toasts").appendChild(toast);
  window.setTimeout(() => toast.remove(), 4800);
}

function render(snapshot) {
  if (!snapshot?.revision || !snapshot.server?.id) return;
  const contextChanged = currentSnapshot?.revision !== snapshot.revision;
  currentSnapshot = snapshot;
  const server = snapshot.server;
  $("#window-context").textContent = server.name || server.id;
  $("#server-name").textContent = server.name || server.id;
  $("#settings-copy").textContent = snapshot.text || t("settings.copy");
  $("#realm-badge").textContent = server.realm || t("settings.noRealm");
  $("#server-address").textContent = server.address || t("settings.noData");
  $("#server-realm").textContent = server.realm || t("settings.noAlias");
  $("#server-client").textContent = server.client_id || t("settings.noData");
  $("#timeout-value").textContent = server.session_timeout_min || 30;
  $("#change-timeout").disabled = !server.can_set_session_timeout;
  $("#change-timeout").title = server.can_set_session_timeout ? t("settings.changeTimeout") : t("settings.unsupportedTimeout");
	const actions = server.actions || [];
	$("#server-actions-card").hidden = actions.length === 0;
	$("#server-actions").innerHTML = actions.map((action) => `<button class="server-action ${escapeHTML(action.tone)}" type="button" data-server-action="${escapeHTML(action.id)}"><span><strong>${labelHTML(action.label_key)}</strong><small>${labelHTML(action.description_key)}</small></span><i aria-hidden="true">›</i></button>`).join("");
  if (contextChanged) window.requestAnimationFrame(() => window.scrollTo(0, 0));
}

async function chooseServerAction(action, button) {
	if (!currentSnapshot?.server?.id) return;
	button.disabled = true;
	try {
		const result = await SettingsService.Choose({ action, server_id: currentSnapshot.server.id });
		if (!result.accepted) showToast(t("ui.unavailable"), result.code || t("settings.changed"));
	} catch (error) {
		showToast(t("ui.sendFailed"), error?.message || String(error));
	} finally {
		window.setTimeout(() => { button.disabled = false; }, 400);
	}
}

async function chooseTimeout() {
  const button = $("#change-timeout");
  if (!currentSnapshot?.server?.id) return;
  button.disabled = true;
  try {
    const result = await SettingsService.Choose({ action: "session_timeout", server_id: currentSnapshot.server.id });
    if (!result.accepted) showToast(t("ui.unavailable"), result.code || t("settings.changed"));
  } catch (error) {
    showToast(t("ui.sendFailed"), error?.message || String(error));
  } finally {
    window.setTimeout(() => { button.disabled = !currentSnapshot?.server?.can_set_session_timeout; }, 400);
  }
}

async function closeSettings() {
  try {
    await SettingsService.Cancel();
  } catch (error) {
    console.debug("Nie udało się zamknąć sesji ustawień", error);
    await Window.Hide();
  }
}

Events.On("filees:settings-snapshot", (event) => render(event?.data ?? event));
$("#change-timeout").addEventListener("click", chooseTimeout);
$("#server-actions").addEventListener("click", (event) => {
	const button = event.target.closest("[data-server-action]");
	if (button) chooseServerAction(button.dataset.serverAction, button);
});
$("#settings-close").addEventListener("click", closeSettings);
$("#settings-done").addEventListener("click", closeSettings);
$("#settings-minimise").addEventListener("click", () => Window.Minimise());
$("#settings-titlebar").addEventListener("dblclick", (event) => {
  if (!event.target.closest(".window-controls")) Window.ToggleMaximise();
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") closeSettings();
});

try {
  render(await SettingsService.Snapshot());
} catch (error) {
  console.error("Nie udało się pobrać ustawień FileES", error);
}
