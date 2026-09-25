// The "what is happening" card of the folder window. Owner's production,
// 2026-09-25: a folder read "wymaga uwagi" while its changes waited for an
// edit passport the server never issued, and neither the window nor the
// journal said what to click. The card names what waits, why, and the one
// step this user can take; everything else stays in the action list below.
//
// Pure: the caller passes the translator and the escaper, so a test renders
// it without the Wails runtime.
export function problemCardHTML(problem, t, escapeHTML) {
  if (!problem || problem.kind !== "borrow_pending") return "";
  const path = problem.path || "—";
  const lines = [`<p>${escapeHTML(t("problem.borrowPending.text", { path }))}</p>`];
  if (problem.holder) lines.push(`<p class="problem-holder">${escapeHTML(t("problem.heldBy", { holder: problem.holder }))}</p>`);
  if (problem.more > 0) lines.push(`<p>${escapeHTML(t("problem.borrowPending.more", { count: problem.more }))}</p>`);
  if (problem.reason && problem.code) lines.push(`<p class="problem-reason">${escapeHTML(t("problem.reason", { reason: problem.reason, code: problem.code }))}</p>`);
  else if (problem.code) lines.push(`<p class="problem-reason">${escapeHTML(t("problem.code", { code: problem.code }))}</p>`);
  const owner = problem.remedy === "disable_editing_lock";
  lines.push(`<p class="problem-remedy">${escapeHTML(t(owner ? "problem.borrowPending.remedyOwner" : "problem.borrowPending.remedyGuest"))}</p>`);
  const button = owner && problem.action_id
    ? `<button class="primary-button" type="button" data-repository-action="${escapeHTML(problem.action_id)}">${escapeHTML(t("repoAction.disable_editing_lock.label"))}</button>`
    : "";
  return `<p class="eyebrow">${escapeHTML(t("problem.eyebrow"))}</p><h2>${escapeHTML(t("problem.borrowPending.title"))}</h2>${lines.join("")}${button}`;
}
