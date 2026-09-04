import { Events, Window } from "/wails/runtime.js";
import { RepositoryService } from "./bindings/filees/cmd/filees-gui-wails/index.js";
import { initializeTheme } from "./theme-preference.js";

initializeTheme();

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
  return `<article class="share-row ${share.channel_id === currentSnapshot?.focus_channel_id ? "is-focused" : ""}" data-share-channel-id="${escapeHTML(share.channel_id)}">
    <div class="share-main"><span class="share-dot ${share.can_revoke ? "active" : ""}" aria-hidden="true"></span><div><strong>${escapeHTML(share.address || share.channel_id)}</strong><small>${escapeHTML(share.source_root || "całe repozytorium")}</small></div></div>
    <div class="share-fact"><small>Stan</small><span>${escapeHTML(share.state || "nieznany")}</span></div>
    <div class="share-fact"><small>Odbiorcy</small><span title="${escapeHTML(share.recipients)}">${escapeHTML(share.recipients || "kanał otwarty")}</span></div>
    <div class="share-fact"><small>Rewizja</small><span>${escapeHTML(share.revision || "HEAD")}</span></div>
    <div class="share-controls">${controls}</div>
  </article>`;
}

function grantAccess(grant) {
  if (!String(grant.state || "").toLowerCase().startsWith("active") && !String(grant.state || "").toLowerCase().startsWith("aktyw")) return "brak dostępu";
  if (grant.access === "rw") return "odczyt i zapis";
  if (grant.access === "r") return "tylko odczyt";
  return grant.access || "brak dostępu";
}

function grantCard(grant) {
  const controls = [
    grant.can_read ? `<button type="button" data-grant-action="grant_read" data-realm-id="${escapeHTML(grant.realm_id)}">Tylko odczyt</button>` : "",
    grant.can_write ? `<button type="button" data-grant-action="grant_write" data-realm-id="${escapeHTML(grant.realm_id)}">Odczyt i zapis</button>` : "",
    grant.can_revoke ? `<button class="danger" type="button" data-grant-action="revoke" data-realm-id="${escapeHTML(grant.realm_id)}">Cofnij</button>` : "",
  ].join("");
  return `<article class="grant-row">
    <div class="grant-main"><span class="share-dot ${grant.can_revoke ? "active" : ""}" aria-hidden="true"></span><div><strong>${escapeHTML(grant.alias || "Strefa FileES")}</strong><small>Odbiorca widoczny w katalogu stref</small></div></div>
    <div class="share-fact"><small>Aktualne uprawnienie</small><span>${escapeHTML(grantAccess(grant))}</span></div>
    <div class="grant-controls">${controls}</div>
  </article>`;
}

function uploadCard(channel) {
	const controls = [
		channel.can_edit ? `<button type="button" data-upload-action="edit" data-channel-id="${escapeHTML(channel.channel_id)}">Edytuj</button>` : "",
		channel.can_revoke ? `<button type="button" data-upload-action="revoke" data-channel-id="${escapeHTML(channel.channel_id)}">Cofnij</button>` : "",
		channel.can_delete ? `<button class="danger" type="button" data-upload-action="delete" data-channel-id="${escapeHTML(channel.channel_id)}">Usuń</button>` : "",
	].join("");
	return `<article class="share-row upload-row">
		<div class="share-main"><span class="share-dot ${channel.can_revoke ? "active" : ""}" aria-hidden="true"></span><div><strong>${escapeHTML(channel.address || channel.channel_id)}</strong><small>${channel.require_otp ? "zamknięta półka · kod z poczty" : "zamknięta półka przyjęcia"}</small></div></div>
		<div class="share-fact"><small>Stan</small><span>${escapeHTML(channel.state || "nieznany")}</span></div>
		<div class="share-fact"><small>Wnoszący</small><span title="${escapeHTML(channel.recipients)}">${escapeHTML(channel.recipients || "brak")}</span></div>
		<div class="share-controls">${controls}</div>
	</article>`;
}

function quarantineCard(item) {
	const verdict = item.av_verdict || "odrzut antywirusa";
	return `<article class="share-row">
		<div class="share-main"><span class="share-dot" aria-hidden="true"></span><div><strong>${escapeHTML(item.original_name || item.upload_id)}</strong><small>${escapeHTML(verdict)}</small></div></div>
		<div class="share-fact"><small>Rozmiar</small><span>${escapeHTML(item.size_label || ((item.size || 0) + " B"))}</span></div>
		<div class="share-fact"><small>TTL</small><span>jeszcze ${escapeHTML(String(item.remaining_hours ?? 0))} godz.</span></div>
		<div class="share-controls">
			<button type="button" data-quarantine-action="fetch" data-upload-id="${escapeHTML(item.upload_id)}">Pobierz</button>
			<button class="danger" type="button" data-quarantine-action="hide" data-upload-id="${escapeHTML(item.upload_id)}">Odrzuć</button>
		</div>
	</article>`;
}

function render(snapshot) {
  if (!snapshot?.revision || !snapshot.context?.repo_id) return;
  const contextChanged = currentSnapshot?.revision !== snapshot.revision;
  currentSnapshot = snapshot;
  const context = snapshot.context;
  const sharesMode = snapshot.mode === "shares";
	const grantsMode = snapshot.mode === "grants";
	const uploadsMode = snapshot.mode === "uploads";
	const quarantineMode = snapshot.mode === "quarantine";
	const detailMode = sharesMode || grantsMode || uploadsMode || quarantineMode;

  $("#window-context").textContent = context.name || context.repo_id;
	$("#scope-label").textContent = sharesMode ? "Folder · udostępnienia" : grantsMode ? "Folder · uprawnienia gości" : uploadsMode ? "Folder · półki przyjęcia" : quarantineMode ? "Folder · kwarantanna" : "Folder FileES";
  $("#repository-name").textContent = context.name || context.repo_id;
  $("#repository-copy").textContent = snapshot.text || "Działania dla tego folderu.";
  $("#repository-server").textContent = context.server_name || context.server_id;
  $("#repository-state").textContent = context.state || "—";
  $("#repository-access").textContent = context.access || "—";
  $("#repository-editing").textContent = context.editing || "—";
	$("#repository-facts").hidden = detailMode;
	$("#actions-view").hidden = detailMode;
  $("#shares-view").hidden = !sharesMode;
	$("#grants-view").hidden = !grantsMode;
	$("#uploads-view").hidden = !uploadsMode;
	$("#quarantine-view").hidden = !quarantineMode;
	$("#back-to-actions").hidden = !detailMode;
	$("#back-to-actions").textContent = sharesMode ? "Zamknij udostępnienia" : grantsMode ? "Zamknij uprawnienia" : uploadsMode ? "Zamknij półki" : quarantineMode ? "Zamknij kwarantannę" : "Zamknij";

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
	if (grantsMode) {
		const grants = snapshot.grants || [];
		$("#realm-grants").innerHTML = grants.length
			? grants.map(grantCard).join("")
			: '<p class="empty">Brak widocznych stref, którym można nadać dostęp.</p>';
	}
	if (uploadsMode) {
		const channels = snapshot.uploads || [];
		$("#upload-channels").innerHTML = channels.length
			? channels.map(uploadCard).join("")
			: '<p class="empty">Ten folder nie ma jeszcze półek przyjęcia.</p>';
		$("#create-upload").disabled = Boolean(snapshot.busy);
	}
	if (quarantineMode) {
		const items = snapshot.quarantine || [];
		$("#quarantine-items").innerHTML = items.length
			? items.map(quarantineCard).join("")
			: '<p class="empty">Poczekalnia jest pusta. Odrzuty znikają same po 48 godzinach.</p>';
	}
  if (contextChanged) window.requestAnimationFrame(() => window.scrollTo(0, 0));
  if (contextChanged && sharesMode && snapshot.focus_channel_id) window.requestAnimationFrame(() => {
    const focused = document.querySelector(`[data-share-channel-id="${CSS.escape(snapshot.focus_channel_id)}"]`);
    focused?.scrollIntoView({ block: "center", behavior: "smooth" });
    focused?.querySelector("button")?.focus();
  });
}

async function chooseGrant(action, realmID, button) {
	if (!currentSnapshot?.context?.repo_id) return;
	button.disabled = true;
	try {
		const choice = contextChoice(action);
		choice.realm_id = realmID;
		const result = await RepositoryService.ChooseGrant(choice);
		if (!result.accepted) showToast("Działanie niedostępne", result.code || "Katalog odbiorców mógł się zmienić.");
	} catch (error) {
		showToast("Nie udało się przekazać intencji", error?.message || String(error));
	} finally {
		window.setTimeout(() => { button.disabled = false; }, 450);
	}
}

async function chooseUpload(action, channelID, button) {
	if (!currentSnapshot?.context?.repo_id) return;
	button.disabled = true;
	try {
		const result = await RepositoryService.ChooseUpload(contextChoice(action, channelID));
		if (!result.accepted) showToast("Działanie niedostępne", result.code || "Lista półek mogła się zmienić.");
	} catch (error) {
		showToast("Nie udało się przekazać intencji", error?.message || String(error));
	} finally {
		window.setTimeout(() => { button.disabled = false; }, 450);
	}
}

async function chooseQuarantine(action, uploadID, button) {
	if (!currentSnapshot?.context?.repo_id) return;
	button.disabled = true;
	try {
		const choice = contextChoice(action);
		choice.upload_id = uploadID;
		const result = await RepositoryService.ChooseQuarantine(choice);
		if (!result.accepted) showToast("Działanie niedostępne", result.code || "Lista kwarantanny mogła się zmienić.");
	} catch (error) {
		showToast("Nie udało się przekazać intencji", error?.message || String(error));
	} finally {
		window.setTimeout(() => { button.disabled = false; }, 450);
	}
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
$("#realm-grants").addEventListener("click", (event) => {
	const button = event.target.closest("[data-grant-action]");
	if (button) chooseGrant(button.dataset.grantAction, button.dataset.realmId || "", button);
});
$("#upload-channels").addEventListener("click", (event) => {
	const button = event.target.closest("[data-upload-action]");
	if (button) chooseUpload(button.dataset.uploadAction, button.dataset.channelId || "", button);
});
$("#create-upload").addEventListener("click", (event) => chooseUpload("create", "", event.currentTarget));
$("#quarantine-items").addEventListener("click", (event) => {
	const button = event.target.closest("[data-quarantine-action]");
	if (button) chooseQuarantine(button.dataset.quarantineAction, button.dataset.uploadId || "", button);
});
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
