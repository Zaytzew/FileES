import { Events, Window } from "/wails/runtime.js";
import { RepositoryService } from "./bindings/filees/cmd/filees-gui-wails/index.js";
import { initializeTheme } from "./theme-preference.js";
import { initializeLanguage, t, labelHTML } from "./i18n.js";
import { readRepoView, saveRepoView, repoViewKey, canArchive } from "./repo-view.js";

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

// Only labels change here: preserve focus, pending actions and their buttons.
function refreshRepositoryLabels() {
  if (!currentSnapshot) return;
  const mode = currentSnapshot.mode;
  const scope = { shares: "sharesScope", grants: "grantsScope", uploads: "uploadScope", quarantine: "quarantineScope" };
  const back = { shares: "repository.back", grants: "repository.closeGrants", uploads: "repository.closeUploads", quarantine: "repository.closeQuarantine" };
  $("#scope-label").textContent = t(`repository.${scope[mode] || "defaultScope"}`);
  $("#repository-copy").textContent = currentSnapshot.text_key ? (currentSnapshot.text_prefix ? currentSnapshot.text_prefix + " " : "") + t(currentSnapshot.text_key) : currentSnapshot.text || t("repository.copy");
  $("#back-to-actions").textContent = t(back[mode] || "action.close");
  document.querySelectorAll("[data-quarantine-hours]").forEach(node => {
    node.textContent = t("quarantine.hoursLeft", {hours: node.dataset.quarantineHours});
  });
}

window.addEventListener("filees:language-changed", refreshRepositoryLabels);

function showToast(title, message = "") {
  const toast = document.createElement("article");
  toast.className = "toast";
  toast.innerHTML = `<strong>${escapeHTML(title)}</strong>${message ? `<span>${escapeHTML(message)}</span>` : ""}`;
  $("#repository-toasts").appendChild(toast);
  window.setTimeout(() => toast.remove(), 5000);
}

function actionButton(action) {
  return `<button class="action-row ${escapeHTML(action.tone)}" type="button" data-repository-action="${escapeHTML(action.id)}">
    <span><strong>${labelHTML(action.label_key)}</strong><small>${labelHTML(action.description_key)}</small></span><i aria-hidden="true">›</i>
  </button>`;
}

function shareCard(share) {
  const controls = [
    share.can_edit ? `<button type="button" data-share-action="edit" data-channel-id="${escapeHTML(share.channel_id)}">${labelHTML("action.edit")}</button>` : "",
    share.can_revoke ? `<button type="button" data-share-action="revoke" data-channel-id="${escapeHTML(share.channel_id)}">${labelHTML("action.revoke")}</button>` : "",
    share.can_delete ? `<button class="danger" type="button" data-share-action="delete" data-channel-id="${escapeHTML(share.channel_id)}">${labelHTML("action.delete")}</button>` : "",
  ].join("");
  return `<article class="share-row ${share.channel_id === currentSnapshot?.focus_channel_id ? "is-focused" : ""}" data-share-channel-id="${escapeHTML(share.channel_id)}">
    <div class="share-main"><span class="share-dot ${share.can_revoke ? "active" : ""}" aria-hidden="true"></span><div><strong>${escapeHTML(share.address || share.channel_id)}</strong><small>${share.source_root ? escapeHTML(share.source_root) : labelHTML("repository.whole")}</small></div></div>
    <div class="share-fact"><small>${labelHTML("field.state")}</small><span>${share.state_key ? labelHTML(share.state_key) : share.state ? escapeHTML(share.state) : labelHTML("field.unknown")}</span></div>
    <div class="share-fact"><small>${labelHTML("field.recipients")}</small><span title="${escapeHTML(share.recipients)}">${share.recipients ? escapeHTML(share.recipients) : labelHTML("share.open")}</span></div>
    <div class="share-fact"><small>${labelHTML("field.revision")}</small><span>${escapeHTML(share.revision || "HEAD")}</span></div>
    <div class="share-controls">${controls}</div>
  </article>`;
}

function grantAccessKey(grant) {
  if (!String(grant.state || "").toLowerCase().startsWith("active") && !String(grant.state || "").toLowerCase().startsWith("aktyw")) return "access.none";
  if (grant.access === "rw") return "access.rw";
  if (grant.access === "r") return "access.r";
  return grant.access ? "" : "access.none";
}

function grantAccess(grant) {
  const key = grantAccessKey(grant);
  return key ? t(key) : grant.access;
}

function grantCard(grant) {
  const controls = [
    grant.can_read ? `<button type="button" data-grant-action="grant_read" data-realm-id="${escapeHTML(grant.realm_id)}">${labelHTML("access.readButton")}</button>` : "",
    grant.can_write ? `<button type="button" data-grant-action="grant_write" data-realm-id="${escapeHTML(grant.realm_id)}">${labelHTML("access.writeButton")}</button>` : "",
    grant.can_revoke ? `<button class="danger" type="button" data-grant-action="revoke" data-realm-id="${escapeHTML(grant.realm_id)}">${labelHTML("action.revoke")}</button>` : "",
  ].join("");
  return `<article class="grant-row">
    <div class="grant-main"><span class="share-dot ${grant.can_revoke ? "active" : ""}" aria-hidden="true"></span><div><strong>${grant.alias ? escapeHTML(grant.alias) : labelHTML("realm.default")}</strong><small>${labelHTML("realm.visibleRecipient")}</small></div></div>
    <div class="share-fact"><small>${labelHTML("realm.permission")}</small><span>${grantAccessKey(grant) ? labelHTML(grantAccessKey(grant)) : escapeHTML(grantAccess(grant))}</span></div>
    <div class="grant-controls">${controls}</div>
  </article>`;
}

function uploadCard(channel) {
	const controls = [
		channel.can_edit ? `<button type="button" data-upload-action="edit" data-channel-id="${escapeHTML(channel.channel_id)}">${labelHTML("action.edit")}</button>` : "",
		channel.can_revoke ? `<button type="button" data-upload-action="revoke" data-channel-id="${escapeHTML(channel.channel_id)}">${labelHTML("action.revoke")}</button>` : "",
		channel.can_delete ? `<button class="danger" type="button" data-upload-action="delete" data-channel-id="${escapeHTML(channel.channel_id)}">${labelHTML("action.delete")}</button>` : "",
	].join("");
	return `<article class="share-row upload-row">
		<div class="share-main"><span class="share-dot ${channel.can_revoke ? "active" : ""}" aria-hidden="true"></span><div><strong>${escapeHTML(channel.address || channel.channel_id)}</strong><small>${labelHTML(channel.require_otp ? "upload.otp" : "upload.closed")}</small></div></div>
		<div class="share-fact"><small>${labelHTML("field.state")}</small><span>${channel.state_key ? labelHTML(channel.state_key) : channel.state ? escapeHTML(channel.state) : labelHTML("field.unknown")}</span></div>
		<div class="share-fact"><small>${labelHTML("upload.contributors")}</small><span title="${escapeHTML(channel.recipients)}">${channel.recipients ? escapeHTML(channel.recipients) : labelHTML("field.none")}</span></div>
		<div class="share-controls">${controls}</div>
	</article>`;
}

function quarantineCard(item) {
	return `<article class="share-row">
		<div class="share-main"><span class="share-dot" aria-hidden="true"></span><div><strong>${escapeHTML(item.original_name || item.upload_id)}</strong><small>${item.av_verdict ? escapeHTML(item.av_verdict) : labelHTML("quarantine.verdict")}</small></div></div>
		<div class="share-fact"><small>${labelHTML("field.size")}</small><span>${escapeHTML(item.size_label || ((item.size || 0) + " B"))}</span></div>
		<div class="share-fact"><small>TTL</small><span data-quarantine-hours="${escapeHTML(String(item.remaining_hours ?? 0))}">${escapeHTML(t("quarantine.hoursLeft", {hours: item.remaining_hours ?? 0}))}</span></div>
		<div class="share-controls">
			<button type="button" data-quarantine-action="fetch" data-upload-id="${escapeHTML(item.upload_id)}">${labelHTML("action.fetch")}</button>
			<button class="danger" type="button" data-quarantine-action="hide" data-upload-id="${escapeHTML(item.upload_id)}">${labelHTML("action.reject")}</button>
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
	$("#scope-label").textContent = sharesMode ? t("repository.sharesScope") : grantsMode ? t("repository.grantsScope") : uploadsMode ? t("repository.uploadScope") : quarantineMode ? t("repository.quarantineScope") : t("repository.defaultScope");
  $("#repository-name").textContent = context.name || context.repo_id;
  $("#repository-copy").textContent = snapshot.text_key ? (snapshot.text_prefix ? snapshot.text_prefix + " " : "") + t(snapshot.text_key) : snapshot.text || t("repository.copy");
  $("#repository-server").textContent = context.server_name || context.server_id;
  $("#repository-state").innerHTML = context.state_key ? labelHTML(context.state_key) : escapeHTML(context.state || "—");
  $("#repository-access").innerHTML = context.access_key ? labelHTML(context.access_key) : escapeHTML(context.access || "—");
  $("#repository-editing").innerHTML = context.editing_key ? labelHTML(context.editing_key) : escapeHTML(context.editing || "—");
	$("#repository-facts").hidden = detailMode;
	$("#actions-view").hidden = detailMode;
  $("#shares-view").hidden = !sharesMode;
	$("#grants-view").hidden = !grantsMode;
	$("#uploads-view").hidden = !uploadsMode;
	$("#quarantine-view").hidden = !quarantineMode;
	$("#back-to-actions").hidden = !detailMode;
	$("#back-to-actions").textContent = sharesMode ? t("repository.back") : grantsMode ? t("repository.closeGrants") : uploadsMode ? t("repository.closeUploads") : quarantineMode ? t("repository.closeQuarantine") : t("action.close");

  if (!sharesMode) {
    const actions = snapshot.actions || [];
    $("#repository-actions").innerHTML = actions.length
      ? actions.map(actionButton).join("")
      : `<p class="empty">${labelHTML("repository.noActions")}</p>`;
    const prefs = readRepoView();
    const archived = prefs.archived[repoViewKey(context)] === context.last_commit_at && Boolean(context.last_commit_at);
    if (!detailMode && (archived || canArchive(context, prefs))) {
      $("#repository-actions").innerHTML += `<button class="action-row" type="button" data-archive-view><span><strong>${labelHTML(archived ? "repository.restoreView" : "repository.archiveView")}</strong><small>${labelHTML("repository.archiveHelp")}</small></span><i aria-hidden="true">›</i></button>`;
    }
  } else {
    const shares = snapshot.shares || [];
    $("#public-shares").innerHTML = shares.length
      ? shares.map(shareCard).join("")
      : `<p class="empty">${labelHTML("repository.noShares")}</p>`;
    $("#create-share").disabled = Boolean(snapshot.busy);
  }
	if (grantsMode) {
		const grants = snapshot.grants || [];
		$("#realm-grants").innerHTML = grants.length
			? grants.map(grantCard).join("")
			: `<p class="empty">${labelHTML("repository.noRealms")}</p>`;
	}
	if (uploadsMode) {
		const channels = snapshot.uploads || [];
		$("#upload-channels").innerHTML = channels.length
			? channels.map(uploadCard).join("")
			: `<p class="empty">${labelHTML("repository.noUploads")}</p>`;
		$("#create-upload").disabled = Boolean(snapshot.busy);
	}
	if (quarantineMode) {
		const items = snapshot.quarantine || [];
		$("#quarantine-items").innerHTML = items.length
			? items.map(quarantineCard).join("")
			: `<p class="empty">${labelHTML("repository.noQuarantine")}</p>`;
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
		if (!result.accepted) showToast(t("ui.unavailable"), result.code || t("repository.recipientsChanged"));
	} catch (error) {
		showToast(t("ui.sendFailed"), error?.message || String(error));
	} finally {
		window.setTimeout(() => { button.disabled = false; }, 450);
	}
}

async function chooseUpload(action, channelID, button) {
	if (!currentSnapshot?.context?.repo_id) return;
	button.disabled = true;
	try {
		const result = await RepositoryService.ChooseUpload(contextChoice(action, channelID));
		if (!result.accepted) showToast(t("ui.unavailable"), result.code || t("repository.uploadsChanged"));
	} catch (error) {
		showToast(t("ui.sendFailed"), error?.message || String(error));
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
		if (!result.accepted) showToast(t("ui.unavailable"), result.code || t("repository.quarantineChanged"));
	} catch (error) {
		showToast(t("ui.sendFailed"), error?.message || String(error));
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
    if (!result.accepted) showToast(t("ui.unavailable"), result.code || t("repository.changed"));
  } catch (error) {
    showToast(t("ui.sendFailed"), error?.message || String(error));
  } finally {
    window.setTimeout(() => { button.disabled = false; }, 450);
  }
}

async function chooseShare(action, channelID, button) {
  if (!currentSnapshot?.context?.repo_id) return;
  button.disabled = true;
  try {
    const result = await RepositoryService.ChooseShare(contextChoice(action, channelID));
    if (!result.accepted) showToast(t("ui.unavailable"), result.code || t("repository.sharesChanged"));
  } catch (error) {
    showToast(t("ui.sendFailed"), error?.message || String(error));
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
  if (event.target.closest("[data-archive-view]")) {
    const context = currentSnapshot?.context;
    if (!context) return;
    const prefs = readRepoView(), key = repoViewKey(context);
    if (prefs.archived[key] === context.last_commit_at) delete prefs.archived[key];
    else if (canArchive(context, prefs)) prefs.archived[key] = context.last_commit_at;
    else return;
    try { saveRepoView(prefs); render(currentSnapshot); showToast(t("view.changed")); }
    catch { showToast(t("view.failed")); }
    return;
  }
  const button = event.target.closest("[data-repository-action]");
  if (button) chooseAction(button.dataset.repositoryAction, button);
});
window.addEventListener("storage", event => { if (event.key === "filees.repo-view.v1" && currentSnapshot) render(currentSnapshot); });
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
