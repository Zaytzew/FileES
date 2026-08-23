import { Events, Window } from "/wails/runtime.js";
import { RepositoryService } from "./bindings/filees/cmd/filees-gui-wails/index.js";

const $ = (selector) => document.querySelector(selector);
const escapeHTML = (value) => String(value ?? "")
  .replaceAll("&", "&amp;")
  .replaceAll("<", "&lt;")
  .replaceAll(">", "&gt;")
  .replaceAll('"', "&quot;")
  .replaceAll("'", "&#039;");

let currentSnapshot = null;

function showToast(title, message = "") {
  const toast = document.createElement("article");
  toast.className = "toast";
  toast.innerHTML = `<strong>${escapeHTML(title)}</strong>${message ? `<span>${escapeHTML(message)}</span>` : ""}`;
  $("#repository-toasts").appendChild(toast);
  window.setTimeout(() => toast.remove(), 5000);
}

function actionButton(action) {
  return `<button class="action-row ${escapeHTML(action.tone)}" type="button" data-repository-action="${escapeHTML(action.id)}">
    <span><strong>${escapeHTML(action.label)}</strong><small>${escapeHTML(action.description)}</small></span><i aria-hidden="true">›</i>
  </button>`;
}

function shareCard(share) {
  const controls = [
    share.can_edit ? `<button type="button" data-share-action="edit" data-channel-id="${escapeHTML(share.channel_id)}">Edytuj</button>` : "",
    share.can_revoke ? `<button type="button" data-share-action="revoke" data-channel-id="${escapeHTML(share.channel_id)}">Cofnij</button>` : "",
    share.can_delete ? `<button class="danger" type="button" data-share-action="delete" data-channel-id="${escapeHTML(share.channel_id)}">Usuń</button>` : "",
  ].join("");
  return `<article class="share-row">
    <div class="share-main"><span class="share-dot ${share.can_revoke ? "active" : ""}" aria-hidden="true"></span><div><strong>${escapeHTML(share.address || share.channel_id)}</strong><small>${escapeHTML(share.source_root || "całe repozytorium")}</small></div></div>
    <div class="share-fact"><small>Stan</small><span>${escapeHTML(share.state || "nieznany")}</span></div>
    <div class="share-fact"><small>Odbiorcy</small><span title="${escapeHTML(share.recipients)}">${escapeHTML(share.recipients || "kanał otwarty")}</span></div>
    <div class="share-fact"><small>Rewizja</small><span>${escapeHTML(share.revision || "HEAD")}</span></div>
    <div class="share-controls">${controls}</div>
  </article>`;
}

function render(snapshot) {
  if (!snapshot?.revision || !snapshot.context?.repo_id) return;
  const contextChanged = currentSnapshot?.revision !== snapshot.revision;
  currentSnapshot = snapshot;
  const context = snapshot.context;
  const sharesMode = snapshot.mode === "shares";

  $("#window-context").textContent = context.name || context.repo_id;
  $("#scope-label").textContent = sharesMode ? "Folder · udostępnienia" : "Folder FileES";
  $("#repository-name").textContent = context.name || context.repo_id;
  $("#repository-copy").textContent = snapshot.text || "Działania dla tego folderu.";
  $("#repository-server").textContent = context.server_name || context.server_id;
  $("#repository-state").textContent = context.state || "—";
  $("#repository-access").textContent = context.access || "—";
  $("#repository-editing").textContent = context.editing || "—";
  $("#repository-facts").hidden = sharesMode;
  $("#actions-view").hidden = sharesMode;
  $("#shares-view").hidden = !sharesMode;
  $("#back-to-actions").hidden = !sharesMode;

  if (!sharesMode) {
    const actions = snapshot.actions || [];
    $("#repository-actions").innerHTML = actions.length
      ? actions.map(actionButton).join("")
      : '<p class="empty">W aktualnym stanie nie ma działań administracyjnych dla tego folderu.</p>';
  } else {
    const shares = snapshot.shares || [];
    $("#public-shares").innerHTML = shares.length
      ? shares.map(shareCard).join("")
      : '<p class="empty">Ten folder nie ma jeszcze publicznych udostępnień.</p>';
    $("#create-share").disabled = Boolean(snapshot.busy);
  }
  if (contextChanged) window.requestAnimationFrame(() => window.scrollTo(0, 0));
}

function contextChoice(action, channelID = "") {
  return {
    action,
    server_id: currentSnapshot?.context?.server_id || "",
    repo_id: currentSnapshot?.context?.repo_id || "",
    channel_id: channelID,
  };
}

async function chooseAction(action, button) {
  if (!currentSnapshot?.context?.repo_id) return;
  button.disabled = true;
  try {
    const result = await RepositoryService.ChooseAction(contextChoice(action));
    if (!result.accepted) showToast("Działanie niedostępne", result.code || "Stan folderu mógł się zmienić.");
  } catch (error) {
    showToast("Nie udało się przekazać intencji", error?.message || String(error));
  } finally {
    window.setTimeout(() => { button.disabled = false; }, 450);
  }
}

async function chooseShare(action, channelID, button) {
  if (!currentSnapshot?.context?.repo_id) return;
  button.disabled = true;
  try {
    const result = await RepositoryService.ChooseShare(contextChoice(action, channelID));
    if (!result.accepted) showToast("Działanie niedostępne", result.code || "Lista udostępnień mogła się zmienić.");
  } catch (error) {
    showToast("Nie udało się przekazać intencji", error?.message || String(error));
  } finally {
    window.setTimeout(() => { button.disabled = false; }, 450);
  }
}

async function closeRepository() {
  try {
    await RepositoryService.Cancel();
  } catch (error) {
    console.debug("Nie udało się zamknąć okna folderu", error);
    await Window.Hide();
  }
}

Events.On("filees:repository-snapshot", (event) => render(event?.data ?? event));
$("#repository-actions").addEventListener("click", (event) => {
  const button = event.target.closest("[data-repository-action]");
  if (button) chooseAction(button.dataset.repositoryAction, button);
});
$("#public-shares").addEventListener("click", (event) => {
  const button = event.target.closest("[data-share-action]");
  if (button) chooseShare(button.dataset.shareAction, button.dataset.channelId || "", button);
});
$("#create-share").addEventListener("click", (event) => chooseShare("create", "", event.currentTarget));
$("#repository-close").addEventListener("click", closeRepository);
$("#repository-done").addEventListener("click", closeRepository);
$("#back-to-actions").addEventListener("click", closeRepository);
$("#repository-minimise").addEventListener("click", () => Window.Minimise());
$("#repository-titlebar").addEventListener("dblclick", (event) => {
  if (!event.target.closest(".window-controls")) Window.ToggleMaximise();
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") closeRepository();
});

try {
  render(await RepositoryService.Snapshot());
} catch (error) {
  console.error("Nie udało się pobrać działań folderu FileES", error);
}
