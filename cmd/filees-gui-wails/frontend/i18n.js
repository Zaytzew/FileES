import pl from "./locales/pl.js";
import en from "./locales/en.js";

// Presentation only: no IPC calls, action IDs or daemon state in this module.
// Add a reviewed catalogue and one registry entry to support another language.
export const languages = Object.freeze([
  { code: "pl", label: "Polski", messages: pl },
  { code: "en", label: "English", messages: en },
]);
const storageKey = "filees.language-preference";
let preference = "system";
let locale = "en";
let channel;
let initialized = false;
const own = (object, key) => Object.prototype.hasOwnProperty.call(object, key);

export function normalizePreference(value) {
  return languages.some(language => language.code === value) ? value : "system";
}

export function resolveLocale(selected, systemLanguages = []) {
  if (normalizePreference(selected) !== "system") return selected;
  // The first language is the UI language, not a list of fallback choices.
  // An unsupported primary system language must fall back to English.
  const primary = String(systemLanguages[0] || "").replaceAll("_", "-").toLowerCase();
  return languages.find(language => primary === language.code || primary.startsWith(`${language.code}-`))?.code || "en";
}

export function getLocale() { return locale; }
export function getLanguagePreference() { return preference; }

export function translate(catalogues, language, key, args = {}) {
  let messages = catalogues[language];
  let message = messages && own(messages, key) ? messages[key] : undefined;
  let messageLocale = language;
  if (message === undefined) {
    message = own(catalogues.en || {}, key) ? catalogues.en[key] : undefined;
    messageLocale = "en";
  }
  if (message && typeof message === "object") {
    const form = new Intl.PluralRules(messageLocale).select(Number(args.count));
    message = message[form] ?? message.other;
  }
  if (typeof message !== "string") return `[${key}]`;
  // Return text, never HTML. Callers inserting into templates must escape it.
  return message.replace(/\{([a-zA-Z][\w]*)\}/g, (_, name) => own(args, name) ? String(args[name]) : `{${name}}`);
}

const catalogues = Object.fromEntries(languages.map(language => [language.code, language.messages]));
export function t(key, args) { return translate(catalogues, locale, key, args); }
export function tn(key, count, args = {}) { return t(key, { ...args, count }); }
export function formatNumber(value, options) { return new Intl.NumberFormat(locale, options).format(value); }
export function formatDate(value, options) { return new Intl.DateTimeFormat(locale, options).format(new Date(value)); }

export function applyTranslations(root = document) {
  root.querySelectorAll("[data-i18n]").forEach(node => {
    // Annotated nodes own only their label, never a form value or user content.
    node.textContent = t(node.dataset.i18n);
  });
  for (const attribute of ["title", "aria-label", "placeholder", "alt", "data-hint"]) {
    root.querySelectorAll(`[data-i18n-${attribute}]`).forEach(node => {
      node.setAttribute(attribute, t(node.getAttribute(`data-i18n-${attribute}`)));
    });
  }
}

function storedPreference() {
  try { return normalizePreference(localStorage.getItem(storageKey)); }
  catch { return "system"; }
}

function applyPreference(value) {
  preference = normalizePreference(value);
  locale = resolveLocale(preference, navigator.languages?.length ? navigator.languages : [navigator.language]);
  document.documentElement.lang = locale;
  document.documentElement.dataset.languagePreference = preference;
  applyTranslations();
  const selector = document.querySelector("#language-preference");
  if (selector) selector.value = preference;
  window.dispatchEvent(new CustomEvent("filees:language-changed", { detail: { preference, locale } }));
}

export function setLanguagePreference(value) {
  const selected = normalizePreference(value);
  try { localStorage.setItem(storageKey, selected); }
  catch { /* Restricted storage: this session and its open windows still work. */ }
  applyPreference(selected);
  channel?.postMessage(selected);
}

export function initializeLanguage() {
  if (initialized) return;
  initialized = true;
  const selector = document.querySelector("#language-preference");
  if (selector) {
    const system = document.createElement("option");
    system.value = "system";
    system.dataset.i18n = "language.system";
    selector.replaceChildren(system, ...languages.map(language => {
      const option = document.createElement("option");
      option.value = language.code;
      option.textContent = language.label;
      return option;
    }));
    selector.addEventListener("change", () => setLanguagePreference(selector.value));
  }
  applyPreference(storedPreference());
  window.addEventListener("languagechange", () => {
    if (preference === "system") applyPreference(preference);
  });
  window.addEventListener("storage", event => {
    if (event.key === storageKey || event.key === null) applyPreference(storedPreference());
  });
  try {
    if ("BroadcastChannel" in window) {
      channel = new BroadcastChannel("filees-language");
      channel.addEventListener("message", event => {
        // Normally storage is authoritative; the payload covers denied storage.
        let value;
        try { value = localStorage.getItem(storageKey) ?? event.data; }
        catch { value = event.data; }
        applyPreference(value);
      });
    }
  } catch { /* Storage events remain available when BroadcastChannel is denied. */ }
}
