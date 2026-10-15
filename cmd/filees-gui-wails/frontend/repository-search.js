// Filter only the existing projection. Never read documents or query a server.
export function searchText(value) {
  return String(value ?? "").normalize("NFD").replace(/\p{M}/gu, "")
    .toLowerCase().replaceAll("ł", "l").trim();
}

export function filterRepositories(repositories, servers, query) {
  const words = searchText(query).split(/\s+/u).filter(Boolean);
  if (!words.length) return repositories;
  const names = new Map(servers.map(server => [server.id,
    [server.display_name, server.realm_alias, server.address, server.id].join(" ")]));
  return repositories.filter(repo => {
    const text = searchText([repo.display_name, repo.own_name, repo.local_path,
      names.get(repo.server_id)].join(" "));
    return words.every(word => text.includes(word));
  });
}
