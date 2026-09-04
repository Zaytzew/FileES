const storageKey = "filees.theme-preference";
let systemTheme = document.documentElement.dataset.systemTheme === "dark" ? "dark" : "light";
let channel = null;

function storedPreference() {
  try {
    const preference = localStorage.getItem(storageKey);
    return preference === "dark" || preference === "light" ? preference : "system";
  } catch {
    return "system";
  }
}

function applyPreference(preference) {
  const selected = preference === "dark" || preference === "light" ? preference : "system";
  document.documentElement.dataset.themePreference = selected;
  document.documentElement.dataset.systemTheme = selected === "system" ? systemTheme : selected;
  window.dispatchEvent(new CustomEvent("filees:theme-changed", {
    detail: { preference: selected, theme: document.documentElement.dataset.systemTheme },
  }));
}

export function initializeTheme() {
  applyPreference(storedPreference());
  window.fileesApplySystemTheme = (theme) => {
    systemTheme = theme === "dark" ? "dark" : "light";
    applyPreference(storedPreference());
  };
  window.addEventListener("storage", (event) => {
    if (event.key === storageKey) applyPreference(storedPreference());
  });
  if ("BroadcastChannel" in window) {
    channel = new BroadcastChannel("filees-theme");
    channel.addEventListener("message", (event) => applyPreference(event.data));
  }
}

export function setThemePreference(preference) {
  preference = preference === "dark" || preference === "light" ? preference : "system";
  try {
    if (preference !== "system") localStorage.setItem(storageKey, preference);
    else localStorage.removeItem(storageKey);
  } catch {
    // A locked-down WebView may deny storage; the current window still works.
  }
  applyPreference(preference);
  channel?.postMessage(preference);
}
