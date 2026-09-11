# Domain language packs

> Pełna procedura dodania języka, razem z katalogiem interfejsu, bramkami
> i pułapkami: `coding-infrastructure/HOWTO_LOCALE.md`.

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

## The three shapes of an entry

A message is a string, a plural object, or an array of variants.

An array is a **ladder**, most specific first. The renderer takes the first
variant whose every parameter is present, so the same key can say "Anna has
rysunek.dwg until 13:41" when the values arrived and still say something
complete when they did not. The last rung must therefore use no parameters
at all, and no rung may ask for everything an earlier rung asks for — it
could never be reached.

A ladder must have the same sequence of parameter sets in every language.
A language shipping three rungs where another ships one is not a wording
difference: one reader is told who is holding the file and the other is told
that somebody is.

A plural entry uses CLDR categories, always including `other`, and is only
valid for a key that declares a `number` parameter. A ladder of plural
objects is not a shape: two selection rules in one entry make it impossible
to tell from the pack which sentence a reader will get.

## Adding a language

1. Copy `en.json` to `<tag>.json`, where `<tag>` is the BCP-47 language tag.
   The file name and the `locale` field must agree.
2. Translate the values, not the keys. Keep the named placeholders identical.
3. Keep `schema` and bump `dictionary_version` when you revise wording.
4. Keep the shape of each entry: a ladder stays a ladder with the same
   sequence of parameter sets, and a plural entry keeps its categories.
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

## Migration status, 2026-09-11

`pl.json` was exported once from `Spec.Polish` as migration material. That
field and `pkg/errcat/present.go` no longer exist: the packs are the only
source of translations, nothing regenerates them from Go, and there is no
second catalogue to fall back to. `pkg/errcat` now carries identity, severity,
hint, typed parameters and the English log diagnostic — nothing a reader sees.

The daemon serves these packs over `messages.catalog`; `pkg/messagerender`
turns a key and its arguments into a sentence. Stage 2 is implemented end to
end and partially accepted on Windows — the remaining gaps are listed in
`todo-control/plan/UNFINISHED_WORK.md`, not here.
