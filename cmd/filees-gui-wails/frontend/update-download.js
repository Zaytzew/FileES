// The background fetch of an available release, as one line under the update
// card and in the version window. Owner's station, 2026-09-25: "Aktualizuj"
// ended in "context deadline exceeded" because the plan fetched the whole
// bundle inside one call; the daemon now fetches it as soon as it sees the
// release, and this line says how far it got.
//
// Pure: the caller passes the translator and the byte formatter.
export function updateDownloadLine(update, t, bytes) {
  if (!update || update.state !== "available") return "";
  switch (update.download) {
    case "downloading":
      return update.download_total > 0
        ? t("version.downloading", { downloaded: bytes(update.downloaded_bytes || 0), total: bytes(update.download_total) })
        : t("version.downloadingUnknown");
    case "ready":
      return t("version.downloaded");
    case "failed":
      return t("version.downloadFailed");
    default:
      return "";
  }
}
