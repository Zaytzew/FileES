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
