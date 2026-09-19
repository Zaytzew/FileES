// GUI-owned semantics. Servers and the daemon only carry this bounded document.
const schema = "filees.gui.drawers/v1";
const idOK = value => typeof value === "string" && /^[a-zA-Z0-9-]{1,80}$/.test(value);
export function emptyDrawers() { return {schema, drawers:[], repos:{}}; }
export function parseDrawers(data) {
  if (!data) return emptyDrawers();
  const value = JSON.parse(data);
  if (value?.schema !== schema || !Array.isArray(value.drawers) || value.drawers.length > 128 || !value.repos || typeof value.repos !== "object" || Array.isArray(value.repos)) throw Error("drawers.invalid");
  const result = emptyDrawers(), ids = new Set();
  for (const drawer of value.drawers) {
    if (!idOK(drawer.id) || ids.has(drawer.id) || typeof drawer.name !== "string" || !drawer.name.trim() || drawer.name.length > 80) throw Error("drawers.invalid");
    result.drawers.push({id:drawer.id,name:drawer.name.trim()}); ids.add(drawer.id);
  }
  if (Object.keys(value.repos).length > 2048) throw Error("drawers.invalid");
  for (const [repo,id] of Object.entries(value.repos)) {
    if (!idOK(repo) || !ids.has(id)) throw Error("drawers.invalid");
    Object.defineProperty(result.repos,repo,{value:id,enumerable:true,writable:true,configurable:true});
  }
  return result;
}
export function changeDrawers(state, action) {
  const next = parseDrawers(JSON.stringify(state));
  if (action.type === "create") {
    const name = String(action.name || "").trim();
    if (!idOK(action.id) || !name || name.length > 80 || next.drawers.length >= 128) throw Error("drawers.invalid");
    if (!next.drawers.some(d => d.id === action.id)) next.drawers.push({id:action.id,name});
  } else if (action.type === "delete") {
    next.drawers = next.drawers.filter(d => d.id !== action.id);
    for (const [repo,id] of Object.entries(next.repos)) if (id === action.id) delete next.repos[repo];
  } else if (action.type === "assign") {
    if (!idOK(action.repo)) throw Error("drawers.invalid");
    if (action.id) {
      if (!next.drawers.some(d => d.id === action.id)) throw Error("drawers.changed");
      Object.defineProperty(next.repos,action.repo,{value:action.id,enumerable:true,writable:true,configurable:true});
    } else delete next.repos[action.repo];
  } else throw Error("drawers.invalid");
  return parseDrawers(JSON.stringify(next));
}
export function groupDrawers(repos, state) {
  const groups = state.drawers.map(d => ({...d,repos:[]}));
  const byID = new Map(groups.map(d => [d.id,d]));
  const loose = [];
  for (const repo of repos) {
    const group = byID.get(Object.hasOwn(state.repos,repo.id) ? state.repos[repo.id] : "");
    (group ? group.repos : loose).push(repo);
  }
  return {groups,loose};
}

// Retry the gesture against the latest document, never the stale whole blob.
export async function commitDrawerChange(initial, action, send) {
  let state = initial;
  for (let attempt=0; attempt<3; attempt++) {
    const value = changeDrawers(parseDrawers(state.data),action);
    const data = JSON.stringify(value);
    if (new TextEncoder().encode(data).length > 32768) throw Error("drawers.invalid");
    const result = await send(state.version,data);
    if (result.scope !== initial.scope) throw Error("drawers.changed");
    parseDrawers(result.data);
    if (!result.conflict) return result;
    state = result;
  }
  throw Error("drawers.changed");
}
