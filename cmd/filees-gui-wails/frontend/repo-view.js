// Presentation preferences only. Dates and eligibility come from daemon projection.
const key = "filees.repo-view.v1";
export function readRepoView() {
  try { const value = JSON.parse(localStorage.getItem(key) || "{}"); return {inactive: validDays(value.inactive,14), archive: validDays(value.archive,30), archived: value.archived && typeof value.archived === "object" && !Array.isArray(value.archived) ? value.archived : {}}; }
  catch { return {inactive:14,archive:30,archived:{}}; }
}
function validDays(value, fallback) { return Number.isInteger(value) && value >= 0 && value <= 36500 ? value : fallback; }
export function saveRepoView(value) { localStorage.setItem(key,JSON.stringify(value)); window.dispatchEvent(new Event("filees:repo-view")); }
export function repoViewKey(repo) { return JSON.stringify([repo.server_id, repo.id || repo.repo_id]); }
export function idleDays(repo, now=Date.now()) { const at=Date.parse(repo.last_commit_at); return Number.isFinite(at) && at > 0 && at <= now ? (now-at)/86400000 : null; }
export function repoOrder(a,b) {
  const at=Date.parse(a.last_commit_at), bt=Date.parse(b.last_commit_at);
  const time=(Number.isFinite(bt)?bt:0)-(Number.isFinite(at)?at:0);
  return time || String(a.display_name||a.id).localeCompare(String(b.display_name||b.id),'pl') || String(a.id).localeCompare(String(b.id));
}
export function repoSection(repo, prefs, now=Date.now()) {
  const age=idleDays(repo,now);
  if (!repo.can_fold_inactive || age===null) return "active";
  if (prefs.archive > 0 && age > prefs.archive && prefs.archived[repoViewKey(repo)] === repo.last_commit_at) return "archived";
  return prefs.inactive > 0 && age > prefs.inactive ? "inactive" : "active";
}
export function canArchive(repo, prefs, now=Date.now()) {
  const age=idleDays(repo,now);
  return repo.can_fold_inactive === true && age !== null && prefs.archive > 0 && age > prefs.archive;
}
