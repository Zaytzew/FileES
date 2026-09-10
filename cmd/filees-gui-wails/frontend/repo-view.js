// Presentation preferences only. Dates and eligibility come from daemon projection.
const key = "filees.repo-view.v1";
export function readRepoView() {
  try { const value = JSON.parse(localStorage.getItem(key) || "{}"); return {inactive: validDays(value.inactive,14), archive: validDays(value.archive,30), archived: marks(value.archived), unarchived: marks(value.unarchived)}; }
  catch { return {inactive:14,archive:30,archived:{},unarchived:{}}; }
}
function marks(value) { return value && typeof value === "object" && !Array.isArray(value) ? value : {}; }
function marked(values, repo) { return Object.hasOwn(values || {}, repoViewKey(repo)) && values[repoViewKey(repo)] === (repo.last_commit_at || ""); }
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
  if (!repo.can_fold_inactive) return "active";
  if (marked(prefs.archived, repo)) return "archived";
  if (age===null) return "active";
  if (prefs.archive > 0 && age > prefs.archive && !marked(prefs.unarchived, repo)) return "archived";
  return prefs.inactive > 0 && age > prefs.inactive ? "inactive" : "active";
}
export function canArchive(repo) {
  return repo.can_fold_inactive === true;
}
export function setArchived(repo, prefs, archived) {
  const id = repoViewKey(repo), stamp = repo.last_commit_at || "";
  if (archived && !canArchive(repo)) return false;
  prefs.archived ||= {};
  prefs.unarchived ||= {};
  if (archived) { prefs.archived[id] = stamp; delete prefs.unarchived[id]; }
  else { delete prefs.archived[id]; prefs.unarchived[id] = stamp; }
  return true;
}
