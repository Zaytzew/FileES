import { Events, Window } from "/wails/runtime.js";
import { PromptService } from "./bindings/filees/cmd/filees-gui-wails/index.js";

const $ = (selector) => document.querySelector(selector);
let snapshot = null;
let resolving = false;

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
  const inputMode = next.mode === "text";
  const infoMode = next.mode === "info";
  $("#prompt-mode").textContent = inputMode ? "Wprowadź dane" : infoMode ? "Informacja FileES" : "Potwierdź działanie";
  $("#prompt-title").textContent = next.title || "FileES";
  $("#prompt-text").textContent = next.text || "";
  $("#input-wrap").hidden = !inputMode;
  $("#prompt-cancel").hidden = infoMode;
  $("#prompt-cancel").textContent = next.cancel_text || "Anuluj";
  $("#prompt-confirm").textContent = next.confirm_text || "Dalej";
  $("#prompt-confirm").disabled = false;
  $("#prompt-cancel").disabled = false;
  const input = $("#prompt-value");
  input.type = next.secret ? "password" : "text";
  input.placeholder = next.placeholder || "";
  input.value = next.default || "";
  document.title = next.title ? `${next.title} — FileES` : "FileES";
  if (inputMode) window.setTimeout(() => { input.focus(); input.select(); }, 80);
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
    const result = await PromptService.Resolve({revision: resolvedRevision, confirmed, value: $("#prompt-value").value});
    if (!result.accepted) throw new Error(result.code || "dialog_rejected");
    // The Go prompt service owns window visibility. A flow may publish the
    // next prompt immediately after Resolve(); hiding here could overtake that
    // Show() and strand the flow in an invisible window.
    void revealFollowingPrompt(resolvedRevision);
  } catch (error) {
    console.error("Nie udało się zamknąć dialogu FileES", error);
    const reason = error?.message || String(error);
    $("#prompt-mode").textContent = `Nie udało się przekazać decyzji · ${reason}`;
    setBusy(false);
  }
}

Events.On("filees:prompt-snapshot", (event) => render(event?.data ?? event));
$("#prompt-form").addEventListener("submit", (event) => { event.preventDefault(); resolve(true); });
$("#prompt-cancel").addEventListener("click", () => resolve(false));
$("#prompt-close").addEventListener("click", () => resolve(false));
$("#prompt-titlebar").addEventListener("dblclick", (event) => { if (!event.target.closest("button")) Window.Center(); });
document.addEventListener("keydown", (event) => { if (event.key === "Escape") { event.preventDefault(); resolve(false); } });

try { render(await PromptService.Snapshot()); } catch (error) { console.error("Nie udało się pobrać dialogu FileES", error); }
