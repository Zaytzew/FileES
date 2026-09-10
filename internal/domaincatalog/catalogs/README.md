# Domain language packs

These are the daemon's domain catalogues: the sentence a person reads for a
known FileES event. They are not the interface catalogue — the Wails renderer
owns its own chrome under `cmd/filees-gui-wails/frontend/locales`, along with
the language preference.

A pack is data compiled into the binary. It is not a plugin: nothing here is
loaded from the disk of a running daemon, and nothing here is executed. Values
are plain text with named parameters; markup is rejected by the validator, and
a renderer inserts a template as text.

## What a pack may and may not say

`pkg/errcat` owns the identity of an error: its code, key, severity, hint and
declared parameters. A pack adds no codes, changes no severity and grants no
actions. Translating a message never changes which operations are available or
which ID travels back over IPC.

Parameters are declared in `pkg/errcat` with a kind, and the kind is what a
renderer is allowed to do with the value:

- `text`, `path`, `identifier` — literal. Never translated, never reformatted.
- `number`, `bytes`, `timestamp` — the **renderer** formats these for its
  reader. The daemon does not send "13:41" or "1.2 MB".
- `diagnostic` — usually `detail`. A renderer may show it, marked as
  diagnostics; a sentence may **not** be built around it. The validator
  rejects a template that places one.

Every language uses the same parameter names for the same key. Word order is
the pack's business; the set of values is not.

## Adding a language

1. Copy `en.json` to `<tag>.json`, where `<tag>` is the BCP-47 language tag.
   The file name and the `locale` field must agree.
2. Translate the values, not the keys. Keep the named placeholders identical.
3. Keep `schema` and bump `dictionary_version` when you revise wording.
4. For a plural entry use CLDR categories, always including `other`. A plural
   entry is only valid for a key that declares a `number` parameter.
5. Run `go test ./internal/domaincatalog` from the repository root. The
   production build runs the same validator, so an incomplete pack fails the
   build instead of shipping behind a fallback.

Discovery is by directory: a new reviewed file plus a rebuild is the whole
procedure. There is no list of languages in the code to edit, and no language
branch anywhere in the logic.

## English is the base

`en.json` is the full base catalogue and the fallback for a reader whose
locale nobody has written yet. It is **not** a licence to ship a partial pack
in a language we do offer — every shipped pack is checked for completeness.

English messages are written for a reader, not copied from `Spec.Diagnostic`.
The diagnostic is the technical log sentence and stays in the log.

## Migration status, 2026-09-10

`pl.json` was exported once from `Spec.Polish` as migration material. From now
on the packs are the only source of translations: nothing regenerates them
from Go, and `Spec.Polish` disappears together with the last call site that
still reads it. Rendering, the IPC snapshot and the switch-over of
`internal/gui/actions` are the next portion of work — this one is the
foundation, not the acceptance of stage 2.
