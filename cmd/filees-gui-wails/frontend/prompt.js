import { Events, Window } from "/wails/runtime.js";
import { PromptService } from "./bindings/filees/cmd/filees-gui-wails/index.js";
import { initializeTheme } from "./theme-preference.js";
import { initializeLanguage, t } from "./i18n.js";

initializeTheme();
initializeLanguage();

const $ = (selector) => document.querySelector(selector);
let snapshot = null;
let resolving = false;
let submissionError = "";

// Only explicitly marked, GUI-authored templates are localized. Unmarked
// daemon messages and diagnostics are displayed verbatim, not matched by text.
function promptText(next, part, original) {
  return next.presentation_key ? t(`${next.presentation_key}.${part}`, next.presentation_args || {}) : original;
}

// A locale change must never call render(): it restores defaults, enables
// buttons and selects input. Update labels only, retaining the pending RPC.
function refreshPromptLabels() {
  if (!snapshot) return;
  const next = snapshot;
  $("#prompt-mode").textContent = submissionError
    ? t("prompt.submitFailed", { reason: submissionError })
    : t(next.mode === "text" ? "prompt.input" : next.mode === "select" ? "prompt.select" : next.mode === "info" ? "prompt.info" : "prompt.confirm");
  $("#prompt-label").textContent = next.mode === "text" ? promptText(next, "label", next.label || t("field.value")) : next.label || t("field.value");
  if (next.mode === "text") $("#prompt-value").placeholder = next.placeholder ? promptText(next, "placeholder", next.placeholder) : "";
  $("#prompt-select-label").textContent = next.label || t("field.server");
  $("#prompt-cancel").textContent = promptText(next, "cancel", next.cancel_text || t("action.cancel"));
  $("#prompt-confirm").textContent = promptText(next, "confirm", next.confirm_text || t("action.continue"));
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
  $("#prompt-mode").textContent = t(inputMode ? "prompt.input" : selectMode ? "prompt.select" : infoMode ? "prompt.info" : "prompt.confirm");
  $("#prompt-title").textContent = next.title || "FileES";
  $("#prompt-text").textContent = next.text || "";
  $("#prompt-label").textContent = next.label || t("field.value");
  $("#input-wrap").hidden = !inputMode;
  $("#prompt-select-label").textContent = next.label || t("field.server");
  $("#select-wrap").hidden = !selectMode;
  $("#prompt-cancel").hidden = infoMode;
  $("#prompt-cancel").textContent = next.cancel_text || t("action.cancel");
  $("#prompt-confirm").textContent = next.confirm_text || t("action.continue");
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
    return node;
  }));
  if (selectMode && next.default) select.value = next.default;
  document.title = next.title ? `${next.title} — FileES` : "FileES";
  refreshPromptLabels();
  if (inputMode) window.setTimeout(() => { input.focus(); input.select(); }, 80);
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
    const value = snapshot.mode === "select" ? $("#prompt-select").value : $("#prompt-value").value;
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
