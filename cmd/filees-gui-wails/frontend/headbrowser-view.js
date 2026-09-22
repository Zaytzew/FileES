// Pure presentation helpers for the "Browse on the server" window, kept apart
// from headbrowser.js so node can test them without a WebView.

// entryType is the badge next to a file: its extension, the way the Android
// list and the public share show types - never a thumbnail read from the file.
export function entryType(name) {
  const text = String(name ?? "");
  const dot = text.lastIndexOf(".");
  if (dot <= 0 || dot === text.length - 1) return "—";
  return text.slice(dot + 1).slice(0, 6).toUpperCase();
}

// Folders first, then files; each by name in the reader's collation.
export function sortEntries(entries, locale) {
  const collator = new Intl.Collator(locale, { numeric: true, sensitivity: "base" });
  return [...entries].sort((a, b) => {
    if ((a.kind === "dir") !== (b.kind === "dir")) return a.kind === "dir" ? -1 : 1;
    return collator.compare(a.name ?? "", b.name ?? "");
  });
}

// copyStatusKey says what is on this computer, so a sparse copy is never
// presented as the whole folder (concept §1a.3).
export function copyStatusKey(repo) {
  if (!repo?.attached) return "headBrowser.statusNone";
  return repo.sparse ? "headBrowser.statusSparse" : "headBrowser.statusFull";
}
