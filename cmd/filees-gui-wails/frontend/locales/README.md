# GUI presentation catalogues

> This is one half of a locale. The daemon's domain packs are the other; the
> whole procedure is in `coding-infrastructure/HOWTO_LOCALE.md`.

This is the Wails renderer's catalogue, not daemon/domain state. No IPC
intent, permission or repository ID may be inferred from translated text.
Catalogue values are plain text. Use `textContent`, or escape `t(...)` when
inserting into an HTML template. Never translate user data or raw diagnostics.

Native Wails menu labels read these same embedded files as JSON data after
the `export default ` marker. Keep the body a strict JSON object followed by
a semicolon: no expressions, imports or JavaScript inside the object. Native
menu lookup currently supports scalar strings only. Main WebView sends its
resolved locale to the host; this event never changes daemon state or replays
notifications. Status/tooltips and fixed notification copy also use this
catalogue. Native named parameters are substituted once as plain text;
native counters use labelled scalar templates, not plural objects.

To add a language:

1. Copy `en.js` to a BCP-47 language file and translate all values, not keys.
2. Keep named placeholders identical; include every plural category returned
   by `Intl.PluralRules(language).resolvedOptions().pluralCategories`.
3. Import it and add one entry with its native display name to `languages`
   in `../i18n.js`. The dropdown is generated from that registry.
4. Run `node --test cmd/filees-gui-wails/frontend-tests/i18n.test.mjs` from
   the repository root and `go test ./cmd/filees-gui-wails`.
5. Check all windows, keyboard/focus and long labels in a real WebView.

System uses the primary WebView UI language and falls back to English when
unsupported. An explicit selection takes precedence. The local preference
is shared with other windows via storage/BroadcastChannel; no daemon restart.

Checkpoint 2026-09-10: static shell in all five pages and selected dynamic
dashboard labels are migrated. Remaining dynamic labels, Go-hosted tray and
dialogs are NOT yet a complete English UI. Do not publish this checkpoint as
completion of stage 1. Domain messages/errcat are stage 2 after acceptance.
