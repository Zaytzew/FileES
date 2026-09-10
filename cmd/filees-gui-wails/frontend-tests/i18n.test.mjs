import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { runInNewContext } from "node:vm";
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

test("only explicitly marked GUI dialog templates are translated", () => {
  const source = readFileSync(new URL("../frontend/prompt.js", import.meta.url), "utf8");
  const start = source.indexOf("function promptText("), end = source.indexOf("\n}", start) + 2;
  const format = runInNewContext(`${source.slice(start, end)}\npromptText`, {
    t: key => translate(catalogues, "en", key),
  });
  // Even an identical Polish sentence from a daemon must stay untouched.
  const raw = catalogues.pl["dialog.restart.text"];
  assert.equal(format({}, "text", raw), raw);
  assert.equal(format({}, "text", "svn_error: Nie można {name} <DWG>"), "svn_error: Nie można {name} <DWG>");
  assert.equal(format({ presentation_key: "dialog.restart" }, "text", raw), catalogues.en["dialog.restart.text"]);
  const controller = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const prefixes = [...controller.matchAll(/PresentationKey:\s*"([^"]+)"/g)].map(match => match[1]);
  assert.equal(prefixes.length, 11); // 10 templates, revoke has two entry points.
  assert.equal(new Set(prefixes).size, 10);
  for (const prefix of prefixes) {
    for (const part of ["title", "text", "confirm", "cancel"]) {
      for (const { messages } of languages) assert.equal(typeof messages[`${prefix}.${part}`], "string");
    }
  }
});

test("marked fixed confirmations keep their Polish fallback and button semantics", () => {
  const controller = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const marked = [...controller.matchAll(/platform\.ConfirmRequest\{\s*PresentationKey:\s*"([^"]+)"([\s\S]*?)\}/g)];
  assert.equal(marked.length, 11);
  for (const [, prefix, body] of marked) {
    for (const [field, part] of [["Title", "title"], ["Text", "text"], ["ConfirmText", "confirm"], ["CancelText", "cancel"]]) {
      const literal = body.match(new RegExp(`${field}:\\s*("(?:\\\\.|[^"\\\\])*")\\s*(?:,|$)`));
      assert.ok(literal, `${prefix}.${part} must be fixed GUI text, not a mixed expression`);
      assert.equal(JSON.parse(literal[1]), catalogues.pl[`${prefix}.${part}`]);
    }
  }
});

test("Go action descriptors resolve in every catalogue without translating operation IDs", () => {
  for (const file of ["repository_service.go", "settings_service.go"]) {
    const source = readFileSync(new URL(`../${file}`, import.meta.url), "utf8");
    const keys = [...source.matchAll(/"((?:repoAction|settingsAction)\.[\w.]+)"/g)].map(match => match[1]);
    assert.ok(keys.length > 10, file);
    for (const key of keys) for (const { messages } of languages) assert.equal(typeof messages[key], "string", key);
  }
  const specs = readFileSync(new URL("../../../internal/gui/platform/settings_flow.go", import.meta.url), "utf8");
  for (const [, id] of specs.matchAll(/\{SettingsDialog\w+, "([^"]+)"/g)) {
    for (const { messages } of languages) assert.equal(typeof messages[`settingsAction.${id}.label`], "string", id);
  }
  const source = readFileSync(new URL("../frontend/repository.js", import.meta.url), "utf8");
  const start = source.indexOf("function actionButton("), end = source.indexOf("function shareCard(", start);
  const button = runInNewContext(`${source.slice(start, end)}\nactionButton`, {
    escapeHTML: value => String(value),
    labelHTML: key => translate(catalogues, "en", key),
  });
  const html = button({ id: "editing_policy", tone: "warning", label_key: "repoAction.disable_editing_lock.label", description_key: "repoAction.disable_editing_lock.description" });
  assert.match(html, /data-repository-action="editing_policy"/);
  assert.match(html, /Disable required reservations/);
  assert.doesNotMatch(html, /undefined/);
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

test("prompt locale change preserves unsaved input and pending submission", async () => {
  const nodes = new Map(), listeners = new Map();
  const node = id => {
    if (!nodes.has(id)) nodes.set(id, { value: "", disabled: false, addEventListener() {}, replaceChildren() {}, focus() {}, select() {} });
    return nodes.get(id);
  };
  let rejectChoice, sent, locale = "pl";
  const source = readFileSync(new URL("../frontend/prompt.js", import.meta.url), "utf8")
    .replace(/^import .*;\r?\n/gm, "")
    .replace(/try \{ render\(await PromptService\.Snapshot\(\)\); \} catch[^\n]*/, "");
  const api = runInNewContext(`${source}\n({ render, resolve, refreshPromptLabels })`, {
    initializeTheme() {}, initializeLanguage() {},
    t: (key, args) => translate(catalogues, locale, key, args),
    document: { querySelector: node, addEventListener() {}, createElement: () => ({}) },
    window: { addEventListener: (name, fn) => listeners.set(name, fn), setTimeout() {} },
    Events: { On() {} }, Window: {}, console: { error() {} },
    PromptService: { Resolve: choice => { sent = choice; return new Promise((_, reject) => { rejectChoice = reject; }); } },
  });
  api.render({ revision: 17, mode: "text", default: "old" });
  const input = node("#prompt-value");
  input.value = "nowa nazwa <DWG>"; input.selectionStart = 4;
  const pending = api.resolve(true);
  locale = "en"; listeners.get("filees:language-changed")();
  assert.equal(input.value, "nowa nazwa <DWG>");
  assert.equal(input.selectionStart, 4);
  assert.equal(node("#prompt-confirm").disabled, true);
  assert.equal(node("#prompt-cancel").disabled, true);
  assert.equal(node("#prompt-confirm").textContent, "Continue");
  assert.equal(sent.revision, 17); assert.equal(sent.confirmed, true);
  assert.equal(sent.value, input.value);
  rejectChoice(new Error("synthetic_failure")); await pending;
  assert.match(node("#prompt-mode").textContent, /synthetic_failure/);
  assert.equal(node("#prompt-confirm").disabled, false);
  locale = "pl"; listeners.get("filees:language-changed")();
  assert.match(node("#prompt-mode").textContent, /synthetic_failure/);
  assert.equal(input.value, "nowa nazwa <DWG>");
});

test("repository translated controls retain opaque action IDs and escape user data", () => {
  const source = readFileSync(new URL("../frontend/repository.js", import.meta.url), "utf8");
  const start = source.indexOf("function shareCard(");
  const end = source.indexOf("function grantAccess(", start);
  const escape = value => String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;").replaceAll("'", "&#039;");
  const i18nSource = readFileSync(new URL("../frontend/i18n.js", import.meta.url), "utf8");
  const labelStart = i18nSource.indexOf("export function labelHTML(");
  const labelEnd = i18nSource.indexOf("\n}", labelStart) + 2;
  const card = runInNewContext(`${i18nSource.slice(labelStart, labelEnd).replace("export ", "")}\n${source.slice(start, end)}\nshareCard`, {
    escapeHTML: escape, currentSnapshot: null,
    t: (key, args) => translate(catalogues, "en", key, args),
  });
  const html = card({ channel_id: 'opaque"<id>', address: "Nazwa użytkownika <DWG>", can_edit: true, can_revoke: true });
  assert.match(html, /data-share-action="edit"/);
  assert.match(html, /data-channel-id="opaque&quot;&lt;id&gt;"/);
  assert.match(html, /data-i18n="action.edit">Edit<\/span>/);
  assert.match(html, /Nazwa użytkownika &lt;DWG&gt;/);
  assert.doesNotMatch(html, /data-share-action="delete"/);
});
