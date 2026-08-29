import { Events, Window } from "/wails/runtime.js";
import { PairingService } from "./bindings/filees/cmd/filees-gui-wails/index.js";

const $ = (selector) => document.querySelector(selector);
let snapshot = null;
let closing = false;
let countdownTimer = null;

function clearPairing() {
  snapshot = null;
  closing = false;
  const qr = $("#pairing-qr");
  qr.removeAttribute("src");
  $("#pairing-address").textContent = "—";
  $("#pairing-countdown").textContent = "—";
  $("#pairing-done").disabled = false;
  if (countdownTimer) window.clearInterval(countdownTimer);
  countdownTimer = null;
}

function updateCountdown() {
  if (!snapshot?.display_until) return;
  const remaining = Math.max(0, new Date(snapshot.display_until).getTime() - Date.now());
  $("#pairing-countdown").textContent = `${Math.ceil(remaining / 1000)} s`;
}

function render(next) {
  if (!next?.active || !next.qr_data_url) {
    clearPairing();
    return;
  }
  snapshot = next;
  closing = false;
  $("#pairing-qr").src = next.qr_data_url;
  $("#pairing-address").textContent = next.address || "—";
  $("#pairing-done").disabled = false;
  if (countdownTimer) window.clearInterval(countdownTimer);
  updateCountdown();
  countdownTimer = window.setInterval(updateCountdown, 250);
  window.setTimeout(() => $("#pairing-done").focus(), 80);
}

async function closePairing() {
  if (!snapshot?.active || closing) return;
  closing = true;
  $("#pairing-done").disabled = true;
  try {
    const result = await PairingService.Close({revision: snapshot.revision});
    if (!result.accepted) throw new Error(result.code || "pairing_close_rejected");
  } catch (error) {
    console.error("Nie udało się zamknąć okna parowania FileES", error);
    closing = false;
    $("#pairing-done").disabled = false;
  }
}

window.fileesClearPairing = clearPairing;
Events.On("filees:pairing-snapshot", (event) => render(event?.data ?? event));
$("#pairing-done").addEventListener("click", closePairing);
$("#pairing-close").addEventListener("click", closePairing);
$("#pairing-titlebar").addEventListener("dblclick", (event) => { if (!event.target.closest("button")) Window.Center(); });
document.addEventListener("keydown", (event) => { if (event.key === "Escape") { event.preventDefault(); closePairing(); } });

try { render(await PairingService.Snapshot()); } catch (error) { console.error("Nie udało się pobrać kodu parowania FileES", error); }
