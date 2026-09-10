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

test("update UI localizes only its fallback, preserving daemon summaries and actions", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const extract = name => {
    const start = source.indexOf(`function ${name}(`);
    return source.slice(start, source.indexOf("\n}", start) + 2);
  };
  const nodes = new Map();
  const node = selector => {
    if (!nodes.has(selector)) nodes.set(selector, {});
    return nodes.get(selector);
  };
  let locale = "en";
  const render = runInNewContext(`${extract("renderVersionDialog")}\n${extract("renderUpdate")}\n({renderVersionDialog, renderUpdate})`, {
    $: node, t: (key, args) => translate(catalogues, locale, key, args),
  });
  render.renderVersionDialog({});
  assert.equal(node("#version-channel").textContent, "not set");
  assert.equal(node("#version-status").textContent, catalogues.en["version.noUpdateInfo"]);
  const literal = 'Demon: {current} <DWG> — nie można wykonać';
  for (const state of ["current", "available", "restart_required"]) {
    const snapshot = { update: { state, summary: literal, current_version: "r1", available_version: "r2", restart_required: state === "restart_required" } };
    for (locale of ["pl", "en"]) {
      render.renderVersionDialog(snapshot);
      render.renderUpdate(snapshot);
      assert.equal(node("#version-status").textContent, literal);
      assert.equal(node("#version-restart-actions").hidden, state !== "restart_required");
      if (state !== "current") assert.equal(node("#update-summary").textContent, literal);
    }
  }
  render.renderVersionDialog({update: {state: "available", available_version: "r2 {current}"}});
  assert.equal(node("#version-status").textContent, "Release r2 {current} is available. Installed release: not set.");
  render.renderUpdate({update: {state: "restart_required", restart_required: true}});
  assert.equal(node("#update-title").textContent, "Restart required");
  assert.equal(node("#update-summary").textContent, "Installed version: unknown.");
});

test("main repository row retains capabilities, identity and raw diagnostics across locales", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const extract = name => {
    const start = source.indexOf(`function ${name}(`);
    return source.slice(start, source.indexOf("\n}", start) + 2);
  };
  let locale = "en";
  const escapeHTML = value => String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll('"', "&quot;");
  const render = runInNewContext(`${extract("repoAction")}\n${extract("renderRepo")}\n${extract("serverHealthPresentation")}\n({renderRepo,serverHealthPresentation})`, {
    t: (key, args) => translate(catalogues, locale, key, args), escapeHTML,
    repoIcons: {}, localizedStates: new Set(["active"]), bytes: String, renderUnportable: () => "",
  });
  const repo = { id: 'id"<raw>', display_name: "Projekt <DWG>", local_path: "E:/Żółć", display_state: "active", can_lock: true };
  for (locale of ["pl", "en"]) {
    const html = render.renderRepo(repo);
    assert.ok(html.includes('data-action="lock"'));
    assert.ok(!html.includes('data-action="unlock"'));
    assert.ok(html.includes('data-repo-id="id&quot;&lt;raw>"'));
    assert.ok(html.includes("Projekt &lt;DWG>"));
    assert.ok(html.includes(catalogues[locale]["repo.lock"]));
    assert.ok(render.renderRepo({...repo, server_deleted: true, cleanup_error: "Błąd <raw>"}).includes("Błąd &lt;raw>"));
    assert.equal(render.serverHealthPresentation("current").className, "health-current");
    assert.equal(render.serverHealthPresentation("current").label, catalogues[locale]["server.health.current"]);
    const cases = [
      [{}, "empty"],
      [{local_provisioning: true}, "importRunning"],
      [{local_provisioning: true, display_state: "offline"}, "importOffline"],
      [{local_provisioning: true, display_state: "attention"}, "importAttention"],
      [{server_deleted: true}, "detached"],
      [{server_deleted: true, local_cleanup_pending: true}, "cleanup"],
      [{server_deleted: true, recovery_pending: true}, "archive"],
      [{server_deleted: true, recovery_pending: true, local_cleanup_pending: true}, "archiveCleanup"],
      [{server_deleted: true, local_copy_preserved: true}, "deletedCheck"],
      [{server_deleted: true, local_copy_preserved: true, local_copy_status: "clean"}, "deletedClean"],
      [{server_deleted: true, local_copy_preserved: true, local_copy_status: "changed"}, "deletedChanged"],
      [{server_deleted: true, local_copy_preserved: true, local_copy_status: "clean", local_cleanup_pending: true}, "deletedCleanup"],
    ];
    for (const [fields, key] of cases) assert.ok(render.renderRepo({...repo, ...fields}).includes(catalogues[locale][`queue.${key}`]), `${locale}:${key}`);
    assert.ok(render.renderRepo({...repo, pending_files: 2, pending_bytes: 123}).includes("2 · 123"));
  }
});

test("action progress translates phase without rewriting supplied labels", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const start = source.indexOf("function renderActions("), end = source.indexOf("\n}", start) + 2;
  const root = {};
  let locale = "en";
  const render = runInNewContext(`${source.slice(start, end)}\nrenderActions`, {
    $: () => root, replaceHTMLIfChanged: (_, html) => root.html = html,
    escapeHTML: value => String(value ?? "").replaceAll("<", "&lt;"),
    t: key => translate(catalogues, locale, key),
  });
  for (locale of ["pl", "en"]) {
    for (const [connected, phase, key] of [[false, "awaiting_projection", "connection"], [true, "awaiting_projection", "projection"], [true, "running", "running"]]) {
      render({connected, pending_actions: [{phase, label: "Etykieta <raw>", repo_id: "Projekt"}]});
      assert.ok(root.html.includes("Etykieta &lt;raw>"));
      assert.ok(root.html.includes(catalogues[locale][`progress.${key}`]));
      assert.equal(root.hidden, false);
    }
    render({pending_actions: []});
    assert.equal(root.hidden, true);
  }
});

test("freshness and partial locks preserve state and literal server details", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const extract = name => { const start = source.indexOf(`function ${name}(`); return source.slice(start, source.indexOf("\n}", start) + 2); };
  const nodes = new Map();
  const node = key => { if (!nodes.has(key)) nodes.set(key, {dataset: {}, classList: {add() {}}}); return nodes.get(key); };
  let locale = "en";
  const render = runInNewContext(`${extract("renderConnection")}\n${extract("renderReservations")}\n({renderConnection,renderReservations})`, {
    $: node, t: (key, args) => translate(catalogues, locale, key, args),
    shortDateTime: String, ageInWords: () => "", escapeHTML: value => String(value ?? "").replaceAll("<", "&lt;"),
    replaceHTMLIfChanged: (root, html) => root.html = html,
  });
  for (locale of ["pl", "en"]) {
    for (const [state, expected] of [["current", "online"], ["refreshing", "stale"], ["server_unavailable", "stale"], ["daemon_offline", "offline"]]) {
      render.renderConnection({connected: true, projection: {state, server_name: "Żółć <raw>", reason: "Błąd {raw}"}});
      assert.equal(node("#pulse-card").dataset.connection, expected);
      if (state === "server_unavailable") assert.equal(node("#pulse-card").dataset.connectionLabel, "Żółć <raw>: Błąd {raw}");
    }
    render.renderReservations({reservation_status: {state: "partial", unavailable: [{display_name: "Żółć <raw>"}], offline: [], stale: []}});
    assert.equal(node("#reservations-card").hidden, false);
    assert.equal(node("#reservations-count").textContent, "0+?");
    assert.ok(node("#reservations").html.includes("Żółć &lt;raw>"));
    render.renderReservations({reservation_status: {state: "current", unavailable: [], offline: [], stale: []}});
    assert.equal(node("#reservations-card").hidden, true);
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
    t: (key, args) => translate(catalogues, "en", key, args),
  });
  // Even an identical Polish sentence from a daemon must stay untouched.
  const raw = catalogues.pl["dialog.restart.text"];
  assert.equal(format({}, "text", raw), raw);
  assert.equal(format({}, "text", "svn_error: Nie można {name} <DWG>"), "svn_error: Nie można {name} <DWG>");
  assert.equal(format({ presentation_key: "dialog.restart" }, "text", raw), catalogues.en["dialog.restart.text"]);
  const controller = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const prefixes = [...controller.matchAll(/PresentationKey:\s*"([^"]+)"/g)].map(match => match[1]);
  assert.equal(prefixes.length, 13); // Revoke has two entry points.
  assert.equal(new Set(prefixes).size, 12);
  for (const prefix of prefixes) {
    for (const part of ["title", "text", "confirm", "cancel"]) {
      for (const { messages } of languages) assert.equal(typeof messages[`${prefix}.${part}`], "string");
    }
  }
});

test("marked fixed confirmations keep their Polish fallback and button semantics", () => {
  const controller = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const parameterized = new Set(["dialog.replaceFile", "dialog.createRepository"]);
  const marked = [...controller.matchAll(/platform\.ConfirmRequest\{\s*PresentationKey:\s*"([^"]+)"([\s\S]*?)\}/g)].filter(match => !parameterized.has(match[1]));
  assert.equal(marked.length, 11);
  for (const [, prefix, body] of marked) {
    for (const [field, part] of [["Title", "title"], ["Text", "text"], ["ConfirmText", "confirm"], ["CancelText", "cancel"]]) {
      const literal = body.match(new RegExp(`${field}:\\s*("(?:\\\\.|[^"\\\\])*")\\s*(?:,|$)`));
      assert.ok(literal, `${prefix}.${part} must be fixed GUI text, not a mixed expression`);
      assert.equal(JSON.parse(literal[1]), catalogues.pl[`${prefix}.${part}`]);
    }
  }
});

test("dialog parameters stay literal and are never submitted as translated choices", () => {
  const source = readFileSync(new URL("../frontend/prompt.js", import.meta.url), "utf8");
  const start = source.indexOf("function promptText("), end = source.indexOf("\n}", start) + 2;
  let locale = "en";
  const format = runInNewContext(`${source.slice(start, end)}\npromptText`, {
    t: (key, args) => translate(catalogues, locale, key, args),
  });
  const name = 'Żółć {path} <img src=x onerror=alert(1)> & "';
  const args = { name, path: 'E:\\Żółć\\{name}', server: 'serwer <test>' };
  const snapshot = { presentation_key: "dialog.createRepository", presentation_args: args };
  const english = format(snapshot, "text", "ignored");
  assert.ok(english.includes(`Name: ${name}`));
  assert.ok(english.includes(`Folder: ${args.path}`));
  locale = "pl";
  assert.ok(format(snapshot, "text", "ignored").includes(`Nazwa: ${name}`));
  assert.equal(args.name, name);
  assert.match(source, /\$\("#prompt-text"\)\.textContent = promptText/);
  const resolve = source.slice(source.indexOf("async function resolve("), source.indexOf('Events.On("filees:prompt-snapshot"'));
  assert.doesNotMatch(resolve, /presentation_args|presentation_key/);
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
