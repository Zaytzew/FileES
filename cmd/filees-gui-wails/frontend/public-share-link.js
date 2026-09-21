// Open only the server-provided recipient entry point. Never invent a URL from
// a display name/SSH address or attach owner credentials or invitation tokens.
export function publicShareURL(value) {
  if (typeof value !== "string" || !value || value.trim() !== value || /[\u0000-\u0020\\]/u.test(value)) return "";
  try {
    const url = new URL(value);
    if (url.protocol !== "https:" || !url.hostname || url.username || url.password || url.search || url.hash) return "";
    return url.href;
  } catch { return ""; }
}

export async function openPublicShare(snapshot, channelID, openURL) {
  if (snapshot?.mode !== "shares") return false;
  const share = snapshot.shares?.find(item => item.channel_id === channelID);
  const url = publicShareURL(share?.public_url);
  if (!url) return false;
  await openURL(url);
  return true;
}
