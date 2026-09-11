import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { runInNewContext } from "node:vm";
import { promptDetailText } from "../frontend/prompt-details.js";
import { languages, resolveLocale, normalizePreference, translate, initializeLanguage, setLanguagePreference, getLocale, t } from "../frontend/i18n.js";

const catalogues = Object.fromEntries(languages.map(language => [language.code, language.messages]));

test("language popup supports selection, dismissal and focus without IPC", () => {
  const handlers = new Map();
  const element = () => ({
    children: [], dataset: {}, hidden: true, attrs: {}, listeners: {},
    append(...items) { this.children.push(...items); },
    addEventListener(name, fn) { this.listeners[name] = fn; },
    setAttribute(k,v) { this.attrs[k] = v; },
    focus() { this.focused = true; },
    contains(node) { return node === this || this.children.some(child => child.contains?.(node)); },
    matches() { return this.type === "radio"; },
  });
  const toggle = element(), popup = element();
  const radios = () => popup.children.map(label => label.children[0]);
  popup.querySelectorAll = () => radios();
  popup.querySelector = () => radios().find(radio => radio.checked);
  const root = {dataset: {languagePreference: "system"}};
  let selected = null, refresh;
  const source = readFileSync(new URL("../frontend/language-menu.js", import.meta.url), "utf8")
    .replace(/^import .*;\r?\n/, "").replace("export function", "function");
  runInNewContext(source + "\ninitializeLanguageMenu();", {
    languages, t: key => translate(catalogues, "en", key),
    setLanguagePreference(value) { selected = value; root.dataset.languagePreference = value; refresh(); },
    document: {documentElement: root, createElement: element,
      querySelector: key => key === "#language-toggle" ? toggle : popup,
      addEventListener: (key, fn) => handlers.set(key,fn)},
    window: {addEventListener: (_,fn) => { refresh = fn; }},
  });
  assert.equal(radios().length, languages.length + 1);
  toggle.listeners.click();
  assert.equal(popup.hidden, false); assert.equal(toggle.attrs["aria-expanded"], "true");
  assert.equal(radios()[0].focused, true);
  const english = radios().find(r => r.value === "en");
  popup.listeners.change({target: english});
  assert.equal(selected, "en"); assert.equal(english.checked, true);
  popup.listeners.click({target: english, detail: 0});
  assert.equal(popup.hidden, false); // keyboard arrows retain the group
  handlers.get("keydown")({key:"Escape",preventDefault(){},stopPropagation(){}});
  assert.equal(popup.hidden, true); assert.equal(toggle.focused, true);
  toggle.listeners.keydown({key:"ArrowDown",preventDefault(){}});
  assert.equal(popup.hidden, false);
  handlers.get("pointerdown")({target: element()});
  assert.equal(popup.hidden, true);
});
const parameters = value => [...new Set([...value.matchAll(/\{([a-zA-Z][\w]*)\}/g)].map(match => match[1]))].sort();

test("GUI native text keys preserve Polish fallback and printf argument contracts", () => {
  const files = ["../../../internal/gui/actions/actions.go", "../../../internal/gui/actions/intent_resolution.go", "../action_bridge.go", "../service.go", "../../../internal/gui/journal/journal.go"];
  let count = 0;
  for (const file of files) {
    const source = readFileSync(new URL(file, import.meta.url), "utf8");
    for (const match of source.matchAll(/(?:uiText|text|chrome)\("([^"]+)", ("(?:[^"\\]|\\.)*")\)/g)) {
      const [, key, quoted] = match, fallback = JSON.parse(quoted);
      assert.equal(catalogues.pl[key], fallback, key);
      const formats = text => [...text.matchAll(/%[sdwqvf]/g)].map(item => item[0]);
      for (const locale of ["pl", "en"]) {
        assert.equal(typeof catalogues[locale][key], "string", key);
        assert.deepEqual(formats(catalogues[locale][key]), formats(fallback), key);
      }
      count++;
    }
  }
  assert.ok(count > 180, count);
});

test("repository language refresh preserves controls and raw quarantine prefix", () => {
  const source = readFileSync(new URL("../frontend/repository.js", import.meta.url), "utf8");
  const begin = source.indexOf("function refreshRepositoryLabels("), end = source.indexOf('\nwindow.addEventListener', begin);
  const nodes = new Map(), node = key => {
    if (!nodes.has(key)) nodes.set(key, {textContent: "", disabled: true});
    return nodes.get(key);
  };
  const hours = {dataset: {quarantineHours: "12"}, textContent: ""};
  let locale = "pl";
  const snapshot = {mode: "quarantine", text_key: "view.quarantine", text_prefix: "Literal <serwer> {hours}"};
  const refresh = runInNewContext(source.slice(begin, end) + "\nrefreshRepositoryLabels", {
    currentSnapshot: snapshot, $: node,
    document: {querySelectorAll: () => [hours]},
    t: (key, args) => translate(catalogues, locale, key, args),
  });
  refresh(); const control = node("#back-to-actions");
  assert.ok(node("#repository-copy").textContent.startsWith(snapshot.text_prefix));
  locale = "en"; refresh();
  assert.equal(node("#back-to-actions"), control);
  assert.equal(control.disabled, true);
  assert.ok(node("#repository-copy").textContent.startsWith(snapshot.text_prefix));
  assert.match(node("#repository-copy").textContent, /Antivirus rejections/);
  assert.equal(hours.textContent, "12 h remaining");
});

test("journal timestamps localize without parsing old presentation strings", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const begin = source.indexOf("function journalTime("), end = source.indexOf("\n}\n", begin) + 2;
  for (const locale of ["pl", "en"]) {
    const format = runInNewContext(source.slice(begin, end) + "\njournalTime", {
      Date, Intl, getLocale: () => locale, t: key => translate(catalogues, locale, key),
    });
    const now = new Date("2026-09-10T12:00:00");
    assert.equal(format({timestamp: "2026-09-10T11:59:40"}, now), catalogues[locale]["time.justNow"]);
    assert.equal(format({timestamp: "2026-09-10T11:57:00"}, now), new Intl.RelativeTimeFormat(locale).format(-3, "minute"));
    assert.equal(format({timestamp: "2026-09-09T11:00:00"}, now), new Intl.RelativeTimeFormat(locale, {numeric: "auto"}).format(-1, "day"));
    assert.equal(format({relative_time: "opaque fallback"}, now), "opaque fallback");
  }
});

test("composed GUI details preserve literal server data and every update variant", () => {
  for (const locale of ["pl", "en"]) {
    const text = (key, args) => translate(catalogues, locale, key, args);
    for (const role of ["ro", "rw"]) for (const creation of ["true", "false"]) {
      const args = {name: "Żółć {name}", address: "<server>", client: "client{port}", port: "2223", role, creation};
      const before = JSON.stringify(args);
      const result = promptDetailText("details.server", args, text);
      for (const value of [args.address, args.client, args.port]) assert.ok(result.includes(value), result);
      assert.ok(result.includes(text(role === "ro" ? "details.readOnly" : "details.full")));
      assert.ok(result.includes(text(creation === "true" ? "details.allowed" : "details.denied")));
      assert.equal(JSON.stringify(args), before);
    }
    assert.ok(promptDetailText("details.server", {}, text).includes(text("details.unknown")));
    for (const key of ["details.update", "details.updateApply"]) {
      assert.ok(promptDetailText(key, {missing: "true"}, text).includes(text("details.update.missing")));
      for (const restart of ["true", "false"]) for (const changes of ["", "PUT <plik> {release}\nraw diagnostyka"]) {
        const args = {current: "r1", available: "r2", release: "alpha{current}", restart, changes};
        const body = promptDetailText(key, args, text);
        assert.ok(body.includes(args.release));
        assert.equal(body.includes(text("details.update.restart")), restart === "true");
        assert.equal(body.includes(text("details.update.question")), key === "details.updateApply");
        assert.ok(body.includes(changes || text("details.update.noChanges")));
      }
    }
    const paths = [{Operation: "add", Path: "<nowy> {size}.dwg", Size: 42}, {Operation: "delete", Path: "stary.dwg", Size: 7}];
    const body = promptDetailText("details.intent", {name: "{path}", paths: JSON.stringify(paths)}, text);
    assert.ok(body.startsWith("{path}\n"));
    assert.ok(body.includes(paths[0].Path));
    assert.ok(body.includes("42"));
    assert.ok(body.includes(paths[1].Path));
    for (const part of ["title", "confirm", "cancel"]) assert.equal(typeof catalogues[locale]["details.intent." + part], "string");
  }
});

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

test("info templates preserve fixed Polish copy and literal diagnostic arguments", () => {
  const controller = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const matches = [...controller.matchAll(/platform\.InfoRequest\{PresentationKey: "(info\.[^"]+)"[^\n]+/g)];
  assert.equal(matches.length, 17);
  for (const [source, key] of matches) {
    const title = JSON.parse(source.match(/Title: ("[^"\n]*")/)[1]);
    assert.equal(catalogues.pl[`${key}.title`], title);
    if (source.includes("PresentationArgs:")) {
      const body = 'SVN: {body} <file> — błąd\nopaque detail';
      for (const locale of ["pl", "en"]) {
        assert.equal(translate(catalogues, locale, `${key}.text`, {body}), body);
      }
    } else {
      const body = JSON.parse(source.match(/Text: ("[^"\n]*")/)[1]);
      assert.equal(catalogues.pl[`${key}.text`], body);
    }
    for (const locale of ["pl", "en"]) {
      for (const part of ["title", "text", "confirm", "cancel"]) {
        assert.equal(typeof catalogues[locale][`${key}.${part}`], "string");
      }
    }
  }
});

test("realm removal results retain dates and separate retention from optional erasure", () => {
  const args = {path: '<kit> {days}', count: 3, downloadUntil: '2026-10-01T12:00:00Z', adminUntil: '2026-11-01T12:00:00Z', days: 90};
  for (const locale of ["pl", "en"]) {
    for (const archives of [false, true]) for (const erasure of [false, true]) {
      const key = `result.realmRemoved${archives ? "Archives" : "Empty"}${erasure ? "Erasure" : ""}`;
      const body = translate(catalogues, locale, `${key}.text`, args);
      assert.equal(body.includes(args.path), archives);
      assert.equal(body.includes(args.downloadUntil), archives);
      assert.equal(body.includes(args.adminUntil), archives);
      assert.equal(body.includes("90"), erasure);
      for (const part of ["title", "text", "confirm", "cancel"]) assert.equal(typeof catalogues[locale][`${key}.${part}`], "string");
    }
  }
  assert.match(catalogues.en["consent.realmRemoval.required.text"], /does not immediately remove/);
  assert.match(catalogues.en["consent.realmRemoval.optional.cancel"], /Without additional request/);
  const source = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  assert.match(source, /if result.ArchiveCount > 0 \{\s*presentationKey = "result.realmRemovedArchives"/);
  assert.match(source, /if result.ErasureRequested \{\s*presentationKey \+= "Erasure"/);
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
    const status = {state: "current", unavailable: [], offline: [], stale: []};
    for (const [fields, action, label] of [
      [{can_release: true, can_request_release: true}, "release_reservation", "release"],
      [{can_request_release: true}, "request_lock_release", "requestRelease"],
      [{lock_release_state: "pending"}, null, "requestSent"],
      [{lock_release_state: "dismissed"}, null, "kept"],
      [{lock_release_state: "accepted"}, null, "releasing"],
      [{}, null, "otherOwner"],
    ]) {
      render.renderReservations({reservation_status: status, reservations: [{id: "opaque-id", path: "Żółć <DWG>", owner_label: "Osoba {raw}", active_passport: true, local_changes: true, ...fields}]});
      const html = node("#reservations").html;
      assert.ok(html.includes('data-reservation-id="opaque-id"'));
      assert.ok(html.includes("Żółć &lt;DWG>"));
      assert.ok(html.includes("Osoba {raw}"));
      assert.ok(html.includes(catalogues[locale][`locks.${label}`]));
      assert.ok(html.includes(catalogues[locale]["locks.localChanges"]));
      assert.ok(html.includes(catalogues[locale]["locks.passport"]));
      if (action) assert.ok(html.includes(`data-action="${action}"`));
      else assert.ok(!html.includes("data-action="));
      if (fields.can_release) assert.ok(!html.includes('data-action="request_lock_release"'));
    }
    render.renderReservations({reservation_status: status, lock_release_requests: [
      {id: "holder-id", role: "holder", state: "pending", path: "Żółć {path} <DWG>", can_accept: true},
      {id: "ignored-id", role: "requester", state: "pending", can_accept: true},
    ]});
    const html = node("#reservations").html;
    assert.ok(html.includes('data-lock-release-request-id="holder-id"'));
    assert.ok(html.includes("Żółć {path} &lt;DWG>"));
    assert.ok(html.includes('data-action="accept_lock_release"'));
    assert.ok(!html.includes('data-action="dismiss_lock_release"'));
    assert.ok(!html.includes("ignored-id"));
  }
});

test("name warnings and card toggles use keys without interpreting user text", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const start = source.indexOf("const unportableReasons ="), end = source.indexOf("function renderRepo(", start);
  const toggleStart = source.indexOf("function updateCardToggleLabel("), toggleEnd = source.indexOf("\n}", toggleStart) + 2;
  let locale = "en";
  const render = runInNewContext(`${source.slice(start, end)}\n${source.slice(toggleStart, toggleEnd)}\n({renderUnportable,updateCardToggleLabel})`, {
    t: (key, args) => translate(catalogues, locale, key, args),
    escapeHTML: value => String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll('"', "&quot;"),
  });
  for (locale of ["pl", "en"]) {
    const html = render.renderUnportable({unportable_names: [{kind: "case_collision", path: 'Żółć"<DWG>', detail: "{count} <raw>"}, {kind: "future_kind", path: "other"}]});
    assert.ok(html.includes('data-action="rename_unportable"'));
    assert.ok(html.includes('data-path="Żółć&quot;&lt;DWG>"'));
    assert.ok(html.includes("{count} &lt;raw>"));
    assert.ok(html.includes(catalogues[locale]["name.unknown"]));
    assert.ok(html.includes(catalogues[locale]["name.rename"]));
    for (const expanded of ["true", "false"]) {
      const attrs = {"aria-expanded": expanded, "aria-label": "arbitrary old text"};
      const toggle = {dataset: {toggleCard: "shouts-body"}, getAttribute: key => attrs[key], setAttribute: (key, value) => attrs[key] = value};
      render.updateCardToggleLabel(toggle);
      const key = `${expanded === "true" ? "collapse" : "expand"}.shouts`;
      assert.equal(attrs["aria-label"], catalogues[locale][key]);
      assert.equal(attrs["data-i18n-aria-label"], key);
      assert.equal(attrs["aria-expanded"], expanded);
    }
  }
});

test("public share renderer translates chrome but preserves names and permission gates", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const start = source.indexOf("function publicShareState("), end = source.indexOf("function unreadAnnouncements(", start);
  const nodes = new Map();
  const node = key => {if (!nodes.has(key)) nodes.set(key, {}); return nodes.get(key);};
  let locale = "en";
  const render = runInNewContext(`${source.slice(start, end)}\nrenderPublicShares`, {
    $: node, t: (key, args) => translate(catalogues, locale, key, args), shortDateTime: String,
    selectedPublicShares: new Set(), selectedPublicShareServer: "", replaceHTMLIfChanged: (root, html) => root.html = html,
    escapeHTML: value => String(value ?? "").replaceAll("<", "&lt;").replaceAll('"', "&quot;"),
  });
  for (locale of ["pl", "en"]) {
    for (const allowed of [true, false]) {
      render({public_shares_known: true, public_shares: [{channel_id: "opaque", server_id: "server", address: "Żółć <DWG>", state: "active", object_count: 2, recipient_count: 3, can_open: allowed, can_revoke: allowed}]});
      const html = node("#public-shares").html;
      assert.ok(html.includes('data-channel-id="opaque"'));
      assert.ok(html.includes("Żółć &lt;DWG>"));
      assert.equal(html.includes('data-action="revoke_public_share"'), allowed);
      assert.equal(html.includes('data-action="manage_public_shares"'), allowed);
      assert.ok(html.includes(catalogues[locale]["share.lifetime"]));
    }
  }
});

test("marked text forms have reviewed fixed text and never translate defaults", () => {
  const source = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const forms = [...source.matchAll(/platform\.PromptTextRequest\{\s*PresentationKey:\s*"(input\.[^"]+)"([\s\S]*?)\}/g)];
  assert.equal(forms.length, 14);
  for (const [, prefix, body] of forms) {
    for (const [field, part] of [["Title", "title"], ["Text", "text"]]) {
      const literal = body.match(new RegExp(`${field}:\\s*("(?:\\\\.|[^"\\\\])*")`));
      assert.ok(literal, `${prefix}.${part}`);
      assert.equal(JSON.parse(literal[1]), catalogues.pl[`${prefix}.${part}`]);
    }
    for (const locale of ["pl", "en"]) for (const part of ["label", "confirm", "cancel"]) assert.equal(typeof catalogues[locale][`${prefix}.${part}`], "string");
    assert.ok(!Object.hasOwn(catalogues.en, `${prefix}.default`));
  }
});

test("all action text prompts are marked and mixed form data remain literal", () => {
  const source = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  assert.doesNotMatch(source, /platform\.PromptTextRequest\{\s*(?!PresentationKey:)\w+:/);
  for (const key of ["form.removeRealmOTP", "form.publish", "form.renameUnportable"]) {
    for (const locale of ["pl", "en"]) {
      for (const part of ["title", "text", "label", "confirm", "cancel"]) assert.equal(typeof catalogues[locale][`${key}.${part}`], "string");
    }
    assert.ok(!Object.hasOwn(catalogues.en, `${key}.default`));
  }
  const path = '"Folder {repos} <DWG>"';
  assert.ok(translate(catalogues, "en", "form.renameUnportable.text", {path}).includes(path));
  const otp = translate(catalogues, "en", "form.removeRealmOTP.text", {repos: 2, grants: 3, clients: 4});
  assert.match(otp, /delete 2 repositories, revoke 3 grants and invalidate 4/);
  assert.match(otp, /do not close FileES/);
});

test("marked input language refresh preserves secret value and pending controls", () => {
  const source = readFileSync(new URL("../frontend/prompt.js", import.meta.url), "utf8");
  const extract = name => { const start = source.indexOf(`function ${name}(`); return source.slice(start, source.indexOf("\n}", start) + 2); };
  const nodes = new Map();
  const node = key => {if (!nodes.has(key)) nodes.set(key, {}); return nodes.get(key);};
  let locale = "pl";
  const refresh = runInNewContext(`${extract("promptText")}\n${extract("refreshPromptLabels")}\nrefreshPromptLabels`, {
    $: node, document: {}, submissionError: "", snapshot: {mode: "text", presentation_key: "input.alias", placeholder: "np. jan-k"},
    t: (key, args) => translate(catalogues, locale, key, args),
  });
  node("#prompt-value").value = "Żółć {secret}";
  node("#prompt-value").type = "password";
  node("#prompt-confirm").disabled = true;
  for (locale of ["pl", "en"]) {
    refresh();
    assert.equal(node("#prompt-value").value, "Żółć {secret}");
    assert.equal(node("#prompt-value").type, "password");
    assert.equal(node("#prompt-confirm").disabled, true);
    assert.equal(node("#prompt-value").placeholder, catalogues[locale]["input.alias.placeholder"]);
    assert.equal(node("#prompt-title").textContent, catalogues[locale]["input.alias.title"]);
  }
});

test("pairing language refresh preserves server selection, PIN and pending state", () => {
  const source = readFileSync(new URL("../frontend/prompt.js", import.meta.url), "utf8");
  const extract = name => { const start = source.indexOf(`function ${name}(`); return source.slice(start, source.indexOf("\n}", start) + 2); };
  const nodes = new Map();
  const node = key => {if (!nodes.has(key)) nodes.set(key, {}); return nodes.get(key);};
  let locale = "pl";
  const snapshot = {mode: "select", presentation_key: "select.pairingServer"};
  const refresh = runInNewContext(`${extract("promptText")}\n${extract("refreshPromptLabels")}\nrefreshPromptLabels`, {
    $: node, document: {}, submissionError: "", snapshot,
    t: (key, args) => translate(catalogues, locale, key, args),
  });
  const options = [{value: "opaque-ID", textContent: "Archiwum <server>"}];
  node("#prompt-select").options = options;
  node("#prompt-select").value = "opaque-ID";
  node("#prompt-value").value = "123456";
  node("#prompt-value").type = "password";
  node("#prompt-confirm").disabled = true;
  for (const key of ["select.pairingServer", "input.pairingPINSetup", "input.pairingPIN", "input.pairingPINRetry"]) {
    snapshot.presentation_key = key;
    snapshot.mode = key.startsWith("select") ? "select" : "text";
    for (locale of ["pl", "en"]) {
      refresh();
      assert.equal(node("#prompt-title").textContent, catalogues[locale][`${key}.title`]);
      assert.equal(node("#prompt-text").textContent, catalogues[locale][`${key}.text`]);
      assert.equal(node(snapshot.mode === "select" ? "#prompt-select-label" : "#prompt-label").textContent, catalogues[locale][`${key}.label`]);
      assert.equal(node("#prompt-select").options, options);
      assert.equal(node("#prompt-select").value, "opaque-ID");
      assert.equal(node("#prompt-value").value, "123456");
      assert.equal(node("#prompt-value").type, "password");
      assert.equal(node("#prompt-confirm").disabled, true);
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
    t: (key, args) => translate(catalogues, "en", key, args),
  });
  // Even an identical Polish sentence from a daemon must stay untouched.
  const raw = catalogues.pl["dialog.restart.text"];
  assert.equal(format({}, "text", raw), raw);
  assert.equal(format({}, "text", "svn_error: Nie można {name} <DWG>"), "svn_error: Nie można {name} <DWG>");
  assert.equal(format({ presentation_key: "dialog.restart" }, "text", raw), catalogues.en["dialog.restart.text"]);
  const controller = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const prefixes = [...controller.matchAll(/PresentationKey:\s*"(dialog\.[^"]+)"/g)].map(match => match[1]);
  assert.equal(prefixes.length, 13); // Revoke has two entry points.
  assert.equal(new Set(prefixes).size, 12);
  for (const prefix of prefixes) {
    for (const part of ["title", "text", "confirm", "cancel"]) {
      for (const { messages } of languages) assert.equal(typeof messages[`${prefix}.${part}`], "string");
    }
  }
});

test("operation confirmations keep literal arguments and explicit risk variants", () => {
  const controller = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const keys = [...new Set([...controller.matchAll(/"(confirm\.[^"]+)"/g)].map(match => match[1]))];
  assert.equal(keys.length, 28);
  for (const key of keys) {
    for (const locale of ["pl", "en"]) {
      for (const part of ["title", "text", "confirm", "cancel"]) assert.equal(typeof catalogues[locale][`${key}.${part}`], "string", key);
      const args = Object.fromEntries(parameters(catalogues[locale][`${key}.text`]).map(name => [name, `<${name}> {opaque} Żółć`]));
      const result = translate(catalogues, locale, `${key}.text`, args);
      for (const value of Object.values(args)) assert.ok(result.includes(value), key);
    }
  }
  assert.match(catalogues.en["confirm.deleteFinal.text"], /0-day retention/);
  assert.match(catalogues.en["confirm.deleteFinal.text"], /no recoverable copy/);
  assert.match(catalogues.en["confirm.releaseRisk.text"], /unsaved data/);
  assert.match(catalogues.en["confirm.releaseAllRisk.text"], /unsaved data/);
  assert.match(catalogues.en["confirm.restoreArchive.text"], /only after confirmed success/);
  assert.equal((controller.match(/presentationKey = "confirm.releaseRisk"/g) || []).length, 2);
  assert.match(controller, /if risky > 0 \{\s*presentationKey = "confirm.releaseAllRisk"/);
});

test("marked fixed confirmations keep their Polish fallback and button semantics", () => {
  const controller = readFileSync(new URL("../../../internal/gui/actions/actions.go", import.meta.url), "utf8");
  const parameterized = new Set(["dialog.replaceFile", "dialog.createRepository"]);
  const marked = [...controller.matchAll(/platform\.ConfirmRequest\{\s*PresentationKey:\s*"(dialog\.[^"]+)"([\s\S]*?)\}/g)].filter(match => !parameterized.has(match[1]));
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

// Raw daemon diagnostics belong in the full journal and nowhere else. The
// activity summary is a glance, not a place to paste machine text into, and
// the tray shows even less. This runs the real renderer rather than reading
// the template, so a future edit that adds item.diagnostics to the preview
// fails here.
test("raw diagnostics reach the full journal and never the activity summary", () => {
  const source = readFileSync(new URL("../frontend/app.js", import.meta.url), "utf8");
  const begin = source.indexOf("function renderJournal("), end = source.indexOf("\n}\n", begin) + 2;
  const nodes = {};
  const context = {
    $: selector => (nodes[selector] ||= {selector, html: ""}),
    replaceHTMLIfChanged: (node, html) => { node.html = html; },
    escapeHTML: value => String(value).replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;"),
    t: key => key,
    journalTime: () => "now",
  };
  const render = runInNewContext(source.slice(begin, end) + "\nrenderJournal", context);
  render({journal: [{
    id: "e1", summary: "[LAB-9999 lab.unknown]", repository: "Dokumenty",
    details: "your decision is needed", diagnostics: "DIAGNOSTIC-LAB-ONLY",
    exact_time: "10:00", emphasized: true,
  }]});

  assert.match(nodes["#journal"].html, /DIAGNOSTIC-LAB-ONLY/);
  assert.match(nodes["#journal"].html, /journal-diagnostics/);
  assert.doesNotMatch(nodes["#activity"].html, /DIAGNOSTIC-LAB-ONLY/);
  // The glance keeps the sentence, so nothing was lost by holding the raw text back.
  assert.match(nodes["#activity"].html, /lab\.unknown/);
});
