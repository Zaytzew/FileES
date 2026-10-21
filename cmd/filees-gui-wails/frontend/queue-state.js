// Presentation only: never derive an empty queue from a runtime that has not
// restored its local cache yet. No extra disk scan or server request is needed.
export function queuePreparing(repo) {
  if (repo.server_deleted || repo.purpose === "upload_shelf" || repo.purpose === "upload_trash") return false;
  if (["offline", "attention", "paused", "stopping", "disabled", "revoked"].includes(repo.display_state)
      || repo.local_copy_waiting_connection || repo.local_copy_presence === "unavailable") return false;
  const local = repo.attached || ["present", "checking"].includes(repo.local_copy_presence);
  return Boolean(local && (!repo.attached || ["initializing", "baselining"].includes(repo.display_state)
    || (repo.cycle && !repo.cycle.phase)));
}

export function queuedChangesLine(repo, t, bytes) {
  if (!repo.pending_files) return "";
  const count = `${repo.pending_files} · ${bytes(repo.pending_bytes)}`;
  const reason = repo.display_state === "attention" ? "queue.attention"
    : repo.display_state === "offline" || repo.local_copy_waiting_connection ? "queue.offline"
    : ["paused", "stopping"].includes(repo.display_state) ? "queue.paused"
    : queuePreparing(repo) ? "queue.loading" : "";
  return reason ? `${count} · ${t(reason)}` : count;
}

export function queueSummary(snapshot, t, bytes) {
  // This is explicitly partial and never includes unverified old-session
  // counts. Other controls remain stale until the full refresh completes.
  if (snapshot.connected && snapshot.queue_scan) {
    const scan = snapshot.queue_scan;
    const amount = scan.files ? t("summary.pendingBytes", {size: bytes(scan.bytes)}) : "";
    return {
      value: scan.files ? `${scan.files}+` : "…",
      note: [amount, t("queue.loading"), `${scan.observed}/${scan.total}`].filter(Boolean).join(" · "),
    };
  }
  const repos = snapshot.repositories || [];
  const pending = repos.reduce((sum, repo) => sum + Number(repo.pending_files || 0), 0);
  const size = repos.reduce((sum, repo) => sum + Number(repo.pending_bytes || 0), 0);
  const cached = !snapshot.connected || snapshot.stale;
  const preparing = !cached && (repos.some(queuePreparing)
    || (!repos.length && (snapshot.servers || []).some(server => server.repositories_ready === false)));
  const attention = repos.some(repo => repo.pending_files > 0 && repo.display_state === "attention");
  const amount = pending ? t("summary.pendingBytes", {size: bytes(size)}) : "";
  const note = cached ? "summary.lastKnown" : preparing ? "queue.loading" : attention ? "queue.attention" : "";
  return {
    value: preparing ? (pending ? `${pending}+` : "…") : pending,
    note: note ? [amount, t(note)].filter(Boolean).join(" · ") : amount || t("summary.emptyQueue"),
  };
}
