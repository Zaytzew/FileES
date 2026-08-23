import { Events, Window } from "/wails/runtime.js";
import { PromptService } from "./bindings/filees/cmd/filees-gui-wails/index.js";

const $ = (selector) => document.querySelector(selector);
let snapshot = null;
let resolving = false;

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

async function resolve(confirmed) {
  if (!snapshot || resolving) return;
  setBusy(true);
  try {
    const result = await PromptService.Resolve({revision: snapshot.revision, confirmed, value: $("#prompt-value").value});
    if (!result.accepted) throw new Error(result.code || "dialog_rejected");
    await Window.Hide();
  } catch (error) {
    console.error("Nie udało się zamknąć dialogu FileES", error);
    $("#prompt-mode").textContent = "Nie udało się przekazać decyzji · spróbuj ponownie";
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
