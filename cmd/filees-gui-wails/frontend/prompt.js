import { Events, Window } from "/wails/runtime.js";
import { PromptService } from "./bindings/filees/cmd/filees-gui-wails/index.js";
import { initializeTheme } from "./theme-preference.js";
import { initializeLanguage, t } from "./i18n.js";
import { promptDetailText } from "./prompt-details.js";

initializeTheme();
initializeLanguage();

const $ = (selector) => document.querySelector(selector);
let snapshot = null;
let resolving = false;
let submissionError = "";

// Only explicitly marked, GUI-authored templates are localized. Unmarked
// daemon messages and diagnostics are displayed verbatim, not matched by text.
function promptText(next, part, original) {
  if (part === "text" && next.presentation_key?.startsWith("details.")) return promptDetailText(next.presentation_key, next.presentation_args || {});
  return next.presentation_key ? t(`${next.presentation_key}.${part}`, next.presentation_args || {}) : original;
}

// The eyebrow names what is being decided. "prompt.select" reads "Choose a
// server", which the version-replacement question showed above its title
// (sandbox, 2026-09-25); a select about something else names it itself.
// Prompts whose option labels are GUI copy, not data, and are translated.
const localizedOptionPrompts = ["select.visibility", "select.updateChannel", "select.replacePredecessor", "select.detachFolder"];

function modeLabelKey(next) {
  const named = {
    "select.updateChannel": "select.updateChannel.label",
    "select.replacePredecessor": "select.replacePredecessor.eyebrow",
    "select.detachFolder": "select.detachFolder.eyebrow",
  }[next.presentation_key];
  if (named) return named;
  return next.mode === "text" ? "prompt.input" : next.mode === "select" ? "prompt.select" : next.mode === "info" ? "prompt.info" : "prompt.confirm";
}

// A locale change must never call render(): it restores defaults, enables
// buttons and selects input. Update labels only, retaining the pending RPC.
function refreshPromptLabels() {
  if (!snapshot) return;
  const next = snapshot;
  $("#prompt-mode").textContent = submissionError
    ? t("prompt.submitFailed", { reason: submissionError })
    : t(modeLabelKey(next));
  $("#prompt-label").textContent = next.mode === "text" ? promptText(next, "label", next.label || t("field.value")) : next.label || t("field.value");
  if (next.mode === "text") $("#prompt-value").placeholder = next.placeholder ? promptText(next, "placeholder", next.placeholder) : "";
  $("#prompt-select-label").textContent = next.mode === "select" ? promptText(next, "label", next.label || t("field.server")) : next.label || t("field.server");
  $("#prompt-cancel").textContent = promptText(next, "cancel", next.cancel_text || t("action.cancel"));
  $("#prompt-confirm").textContent = promptText(next, "confirm", next.confirm_text || t(next.mode === "info" ? "action.understood" : "action.continue"));
  $("#prompt-title").textContent = promptText(next, "title", next.title || "FileES");
  $("#prompt-text").textContent = promptText(next, "text", next.text || "");
  document.title = `${$("#prompt-title").textContent} — FileES`;
}

const wait = (milliseconds) => new Promise((resolve) => window.setTimeout(resolve, milliseconds));

function setBusy(busy) {
  resolving = busy;
  $("#prompt-confirm").disabled = busy;
  $("#prompt-cancel").disabled = busy;
  $("#prompt-close").disabled = busy;
}

function render(next) {
  if (!next?.revision) return;
  snapshot = next;
  resolving = false;
  submissionError = "";
  const inputMode = next.mode === "text";
  const selectMode = next.mode === "select";
  const infoMode = next.mode === "info";
  $("#prompt-mode").textContent = t(modeLabelKey(next));
  $("#prompt-title").textContent = next.title || "FileES";
  $("#prompt-text").textContent = next.text || "";
  $("#prompt-label").textContent = next.label || t("field.value");
  $("#input-wrap").hidden = !inputMode;
  $("#prompt-select-label").textContent = next.label || t("field.server");
  // A radio prompt shows every option at once (owner, 2026-09-28: the
  // detach choice "Zachowaj lokalny folder" / "Usuń również lokalny folder").
  const radioMode = selectMode && Boolean(next.radio);
  $("#select-wrap").hidden = !selectMode || radioMode;
  $("#radio-wrap").hidden = !radioMode;
  $("#prompt-radio-label").textContent = radioMode ? (next.label || "") : "";
  $("#prompt-radio").replaceChildren(...(radioMode ? next.options || [] : []).map((option) => {
    const label = document.createElement("label");
    label.className = "choice";
    const input = document.createElement("input");
    input.type = "radio";
    input.name = "prompt-choice";
    input.value = option.value;
    input.checked = option.value === (next.default || next.options[0]?.value);
    const text = document.createElement("span");
    text.textContent = option.label;
    if (localizedOptionPrompts.includes(next.presentation_key)) {
      text.dataset.i18n = next.presentation_key + "." + option.value;
      text.textContent = t(text.dataset.i18n);
    }
    label.append(input, text);
    return label;
  }));
  $("#prompt-cancel").hidden = infoMode;
  $("#prompt-cancel").textContent = next.cancel_text || t("action.cancel");
  $("#prompt-confirm").textContent = next.confirm_text || t(next.mode === "info" ? "action.understood" : "action.continue");
  $("#prompt-confirm").disabled = false;
  $("#prompt-cancel").disabled = false;
  const input = $("#prompt-value");
  input.type = next.secret ? "password" : "text";
  input.placeholder = next.placeholder || "";
  input.value = next.default || "";
  const select = $("#prompt-select");
  select.replaceChildren(...(next.options || []).map((option) => {
    const node = document.createElement("option");
    node.value = option.value;
    node.textContent = option.detail && option.detail !== option.label ? `${option.label} — ${option.detail}` : option.label;
    if (localizedOptionPrompts.includes(next.presentation_key)) {
      node.dataset.i18n = next.presentation_key + "." + option.value;
      node.textContent = t(node.dataset.i18n);
    }
    return node;
  }));
  if (selectMode && next.default) select.value = next.default;
  document.title = next.title ? `${next.title} — FileES` : "FileES";
  refreshPromptLabels();
  if (inputMode) window.setTimeout(() => { input.focus(); input.select(); }, 80);
  else if (radioMode) window.setTimeout(() => $("#prompt-radio input:checked")?.focus(), 80);
  else if (selectMode) window.setTimeout(() => select.focus(), 80);
  else window.setTimeout(() => $("#prompt-confirm").focus(), 80);
}

async function revealFollowingPrompt(resolvedRevision) {
  // Resolve() wakes the Go controller before its RPC response necessarily
  // reaches this WebView. The controller may therefore publish the following
  // prompt while this page is still completing the previous submit. Events are
  // only a wake-up hint; Snapshot is the authoritative hand-off.
  for (const delay of [0, 25, 75, 150, 300]) {
    if (delay) await wait(delay);
    let next = snapshot?.revision > resolvedRevision ? snapshot : null;
    if (!next) {
      try { next = await PromptService.Snapshot(); }
      catch (error) { console.error("Nie udało się odświeżyć kolejnego dialogu FileES", error); }
    }
    if (next?.revision > resolvedRevision) {
      if (snapshot?.revision !== next.revision) render(next);
      await Promise.allSettled([Window.Show(), Window.Focus()]);
      return;
    }
  }
}

async function resolve(confirmed) {
  if (!snapshot || resolving) return;
  const resolvedRevision = snapshot.revision;
  setBusy(true);
  try {
    const value = snapshot.mode !== "select" ? $("#prompt-value").value
      : snapshot.radio ? ($("#prompt-radio input:checked")?.value || "") : $("#prompt-select").value;
    const result = await PromptService.Resolve({revision: resolvedRevision, confirmed, value});
    if (!result.accepted) throw new Error(result.code || "dialog_rejected");
    // The Go prompt service owns window visibility. A flow may publish the
    // next prompt immediately after Resolve(); hiding here could overtake that
    // Show() and strand the flow in an invisible window.
    void revealFollowingPrompt(resolvedRevision);
  } catch (error) {
    console.error("Nie udało się zamknąć dialogu FileES", error);
    const reason = error?.message || String(error);
    submissionError = reason;
    refreshPromptLabels();
    setBusy(false);
  }
}

Events.On("filees:prompt-snapshot", (event) => render(event?.data ?? event));
window.addEventListener("filees:language-changed", refreshPromptLabels);
$("#prompt-form").addEventListener("submit", (event) => { event.preventDefault(); resolve(true); });
$("#prompt-cancel").addEventListener("click", () => resolve(false));
$("#prompt-close").addEventListener("click", () => resolve(false));
$("#prompt-titlebar").addEventListener("dblclick", (event) => { if (!event.target.closest("button")) Window.Center(); });
document.addEventListener("keydown", (event) => { if (event.key === "Escape") { event.preventDefault(); resolve(false); } });

try { render(await PromptService.Snapshot()); } catch (error) { console.error("Nie udało się pobrać dialogu FileES", error); }
