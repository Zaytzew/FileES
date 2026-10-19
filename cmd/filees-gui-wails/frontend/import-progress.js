import { conversionProgressLine } from "./conversion-progress.js";
// Reuse the daemon's measured first-publication counters, not the global queue.
// A complete byte count is still publication progress, not an activation receipt.
export function importProgressLine(repo, t, bytes) {
  const total = Number(repo.publish_files_total);
  if (!repo.local_provisioning || !Number.isFinite(total) || total <= 0) return "";
  return t("progress.published", {
    done: Number(repo.publish_files_done) || 0, total,
    sent: bytes(Number(repo.publish_bytes_sent) || 0),
    size: bytes(Number(repo.publish_bytes_total) || 0),
  });
}

export function reconcileProgressLine(repo, t) {
  const conversion = conversionProgressLine(repo, t);
  if (conversion) return conversion;
  const p = repo.reconcile_progress;
  if (!p) return "";
  if (p.phase === "planning") return t("progress.reconcile.planning");
  if (p.phase !== "adopting" || !Number.isInteger(p.done) || !Number.isInteger(p.total) || p.total < 0 || p.done < 0 || p.done > p.total) return "";
  return t("progress.reconcile.adopting", {done: p.done, total: p.total});
}
