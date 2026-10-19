// Daemon-owned receipts/counters, never estimated percentages. Text is escaped
// by the caller along with the rest of the queue column.
export function conversionProgressLine(repo, t) {
  const p = repo.conversion;
  if (!p?.operation_id || ["complete", "canceled", "rolled_back"].includes(p.phase)) return "";
  const phases = new Set(["waiting", "inspecting", "retaining", "retained", "creating", "preserving", "verifying", "cleanup", "rolling_back", "finalizing"]);
  let text = phases.has(p.phase) ? t(`conversion.phase.${p.phase}`) : t("conversion.phase.unknown");
  if (p.counters_known && ["preserving", "cleanup"].includes(p.phase) && Number.isSafeInteger(p.done) && Number.isSafeInteger(p.total) && p.done >= 0 && p.total >= p.done) {
    text += " · " + t("conversion.count", { done: p.done, total: p.total });
  }
  if (p.error) text = t("conversion.stopped") + " · " + text + " · " + p.error;
  if (p.error && p.recovery_path) text += " · " + t("conversion.recovery", { path: p.recovery_path });
  return text;
}
