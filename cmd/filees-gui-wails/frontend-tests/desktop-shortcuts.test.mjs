import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
import {languages} from '../frontend/i18n.js';

const app = readFileSync(new URL('../frontend/app.js', import.meta.url), 'utf8');

test('shelf shortcut uses the existing browser icon and regular controller gesture', () => {
  assert.match(app, /repo\.can_browse_shelf \? repoAction\("browse_shelf", t\("action.browseShelf"\), repoIcons\.browse/);
  for (const {messages} of languages) assert.ok(messages['action.browseShelf']);
});

test('cloud control presents global preference, disabled slash and busy guard', () => {
  assert.match(app, /snapshot\.virtual_folders \? `<button/);
  assert.match(app, /aria-pressed="\$\{snapshot\.virtual_folders\.enabled\}"/);
  assert.match(app, /snapshot\.virtual_folders\.enabled \? "" : '<path d="M3 3l18 18"/);
  assert.match(app, /virtualFoldersBusy \? "disabled"/);
  assert.match(app, /event\.target\.closest\("\[data-virtual-folders\]"\)/);
});

test('cloud gesture toggles once, does not guess state and refreshes after lost answer', async () => {
  const source = app.slice(app.indexOf('let virtualFoldersBusy ='), app.indexOf('// After a demo realm'));
  for (const accepted of [true, false, 'lost']) {
    let calls = 0, refreshes = 0;
    const snapshot = {revision: 17, virtual_folders: {enabled: false}};
    const scope = {
      currentSnapshot: snapshot, renderRepositories() {}, t: key => key, showToast() {},
      SettingsService: {async DashboardVirtualFolders(enabled, revision) {
        calls++; assert.equal(enabled, true); assert.equal(revision, 17);
        if (accepted === 'lost') throw Error('lost');
        return {accepted};
      }},
      GUIService: {async Refresh() { refreshes++; }},
    };
    runInNewContext(source+'; globalThis.toggle = toggleVirtualFolders;', scope);
    await Promise.all([scope.toggle(), scope.toggle()]);
    assert.equal(calls, 1);
    assert.equal(refreshes, 1);
    assert.equal(snapshot.virtual_folders.enabled, false);
  }
});

test('local folder name prompts and validation have all five translations', () => {
  for (const {code, messages} of languages) {
    for (const suffix of ['title', 'text', 'label', 'confirm', 'cancel']) assert.ok(messages['input.localFolderName.'+suffix], code);
    assert.ok(messages['feedback.localFolderNameInvalid'], code);
  }
});
