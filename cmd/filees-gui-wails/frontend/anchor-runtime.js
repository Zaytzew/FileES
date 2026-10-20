// Provider readiness is not SVN connectivity or a transfer percentage.
export function anchorRuntimeMarkup(repo, t, escape, dateTime) {
  if (!repo.attached || repo.server_deleted || !repo.anchor_runtime) return "";
  const {phase, since, detail} = repo.anchor_runtime;
  if (phase === "ready") return "";
  const known = ["preparing", "connecting", "failed"].includes(phase);
  const label = known ? phase : "unknown";
  const timestamp = since && Number.isFinite(Date.parse(since))
    ? ` ${escape(t("anchorRuntime.since", {time: dateTime(since)}))}` : "";
  const diagnostic = phase === "failed" && detail
    ? `<details><summary>${escape(t("anchorRuntime.diagnostic"))}</summary><pre>${escape(detail)}</pre></details>` : "";
  return `<div class="repo-problem" role="status"><strong>${escape(t(`anchorRuntime.${label}`))}</strong>${timestamp}<small>${escape(t(phase === "failed" ? "anchorRuntime.failureHelp" : "anchorRuntime.waitHelp"))}</small>${diagnostic}</div>`;
}
