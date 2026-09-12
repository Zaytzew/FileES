// Presentation only: an explicit relation from the daemon, scoped by server.
export function shelvesFor(parent, repositories) {
  if (parent.purpose || parent.server_deleted) return [];
  return repositories.filter(child => child.server_id === parent.server_id && child.parent_repo_id === parent.id && child.purpose === "upload_shelf" && !child.server_deleted);
}

export function unparentedShelves(repositories) {
  return repositories.filter(child => child.purpose === "upload_shelf" && !child.server_deleted &&
    !repositories.some(parent => parent.server_id === child.server_id && parent.id === child.parent_repo_id && !parent.purpose && !parent.server_deleted));
}
