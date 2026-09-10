import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { languages, resolveLocale, normalizePreference, translate, initializeLanguage, setLanguagePreference, getLocale, t } from "../frontend/i18n.js";

const catalogues = Object.fromEntries(languages.map(language => [language.code, language.messages]));
const parameters = value => [...new Set([...value.matchAll(/\{([a-zA-Z][\w]*)\}/g)].map(match => match[1]))].sort();

test("all registered catalogues have matching keys, arguments and plural forms", () => {
  assert.equal(new Set(languages.map(item => item.code)).size, languages.length);
  const keys = Object.keys(catalogues.en).sort();
  for (const { code, messages } of languages) {
    assert.deepEqual(Object.keys(messages).sort(), keys);
    for (const key of keys) {
      const value = messages[key], reference = catalogues.en[key];
      assert.equal(typeof value, typeof reference, key);
      const variants = typeof value === "string" ? [value] : Object.values(value);
      if (typeof value === "object") {
        for (const category of new Intl.PluralRules(code).resolvedOptions().pluralCategories) {
          assert.equal(typeof value[category], "string", `${code}:${key}:${category}`);
        }
      }
      for (const variant of variants) {
        assert.ok(variant.length, key);
        assert.deepEqual(parameters(variant), parameters(typeof reference === "string" ? reference : reference.other), `${code}:${key}`);
      }
    }
  }
});

test("system locale and English fallback do not depend on catalogue order", () => {
  assert.equal(resolveLocale("system", ["pl-PL"]), "pl");
  assert.equal(resolveLocale("system", ["en-GB"]), "en");
  assert.equal(resolveLocale("system", ["de-DE", "pl-PL"]), "en");
  assert.equal(resolveLocale("system", []), "en");
  assert.equal(resolveLocale("pl", ["en-US"]), "pl");
  assert.equal(normalizePreference("../../private"), "system");
  assert.equal(resolveLocale("system", ["PL_pl"]), "pl");
});

test("plural, named arguments and missing keys stay plain text", () => {
  assert.equal(translate(catalogues, "pl", "count.folders", { count: 1 }), "1 folder");
  assert.equal(translate(catalogues, "pl", "count.folders", { count: 2 }), "2 foldery");
  assert.equal(translate(catalogues, "pl", "count.folders", { count: 12 }), "12 folderów");
  assert.equal(translate(catalogues, "en", "count.folders", { count: 2 }), "2 folders");
  assert.equal(translate({ en: { item: { one: "one", other: "many" } }, pl: {} }, "pl", "item", { count: 2 }), "many");
  assert.equal(translate(catalogues, "en", "missing"), "[missing]");
  assert.equal(translate(catalogues, "en", "toString"), "[toString]");
  assert.equal(translate(catalogues, "en", "projection.revision"), "state #{revision}");
  assert.equal(translate(catalogues, "en", "projection.revision", { revision: "<img onerror=x>" }), "state #<img onerror=x>");
});

test("static HTML annotations resolve and never contain HTML in catalogues", () => {
  for (const page of ["index", "settings", "repository", "prompt", "pairing"]) {
    const html = readFileSync(new URL(`../frontend/${page}.html`, import.meta.url), "utf8");
    for (const [, key] of html.matchAll(/data-i18n(?:-[\w-]+)?="([^"]+)"/g)) {
      assert.equal(typeof catalogues.en[key], "string", `${page}:${key}`);
    }
  }
  for (const messages of Object.values(catalogues)) {
    for (const value of Object.values(messages)) {
      for (const text of typeof value === "string" ? [value] : Object.values(value)) assert.ok(!/<\/?[a-z]/i.test(text));
    }
  }
});

test("preference changes synchronize, survive denied storage and do not touch input", () => {
  let stored = "pl", deny = false, channel;
  const listeners = new Map();
  const label = { dataset: { i18n: "action.cancel" }, textContent: "Anuluj" };
  const input = { value: "unsaved DWG name", selectionStart: 3, disabled: true };
  const selector = { value: "", options: [], replaceChildren(...nodes) { this.options = nodes; }, addEventListener() {} };
  globalThis.document = {
    documentElement: { lang: "", dataset: {} }, activeElement: input,
    createElement: () => ({ dataset: {} }),
    querySelector: id => id === "#language-preference" ? selector : null,
    querySelectorAll: query => query === "[data-i18n]" ? [label, ...selector.options.filter(option => option.dataset.i18n)] : [],
  };
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: { languages: ["pl-PL"] } });
  globalThis.localStorage = { getItem() { if (deny) throw Error("denied"); return stored; }, setItem(_, value) { if (deny) throw Error("denied"); stored = value; } };
  globalThis.CustomEvent = class { constructor(type, options) { this.type = type; this.detail = options.detail; } };
  globalThis.BroadcastChannel = class {
    constructor() { channel = this; }
    addEventListener(_, callback) { this.receive = callback; }
    postMessage(value) { this.sent = value; }
  };
  globalThis.window = {
    BroadcastChannel,
    addEventListener(name, callback) { listeners.set(name, callback); },
    dispatchEvent(event) { listeners.get(event.type)?.(event); },
  };
  initializeLanguage();
  assert.equal(getLocale(), "pl");
  assert.equal(selector.options.length, languages.length + 1);
  setLanguagePreference("en");
  assert.equal(stored, "en"); assert.equal(label.textContent, "Cancel");
  assert.equal(selector.value, "en"); assert.equal(channel.sent, "en");
  assert.equal(t("action.cancel"), "Cancel");
  stored = "pl"; listeners.get("storage")({ key: "filees.language-preference" });
  assert.equal(label.textContent, "Anuluj");
  deny = true; setLanguagePreference("en");
  assert.equal(label.textContent, "Cancel");
  channel.receive({ data: "pl" }); assert.equal(label.textContent, "Anuluj");
  setLanguagePreference("system");
  navigator.languages = ["fr-FR"]; listeners.get("languagechange")();
  assert.equal(getLocale(), "en");
  assert.deepEqual(input, { value: "unsaved DWG name", selectionStart: 3, disabled: true });
  assert.equal(document.activeElement, input);
});
