# FileES Android client — Etap 6

Surfaces follow the Wails colour roles in `res/values/colors.xml` (and
`values-night/`): canvas, panel, text, muted, line, orange accent, destructive.
The toolbar stays navy for the wordmark. Do not treat Material3 defaults as
the FileES palette.

This is the Kotlin/Gradle side of the mobile client from
`implementation notes (not distributed)`. The Go core (protocol,
worker-side dispatcher, local store, upload queue, embedded SSH transport)
lives in `pkg/mobile/v1`, `internal/mobileworker` and `pkg/mobileclient`; this
directory is only the Kotlin shell around the `pkg/mobileclient/androidbind`
gomobile binding — see `implementation notes (not distributed)` §17 and
`implementation notes (not distributed)` for how that side was
built and verified.

## What's here

- `app/src/main/java/net/filees/mobile/ManifestCacheProvider.kt` — a
  read-only `content://` provider over the local manifest cache
  (`androidbind.Store`), `exported=false`.
- `app/src/main/java/net/filees/mobile/MainActivity.kt` — pairing (QR) plus
  the operational screen. After pairing, the phone loads the realm
  projection (`LIST_REPOSITORIES`) and offers a share picker. Mobile never
  creates repositories and never asks the operator to type a repo UUID.
- `app/libs/filees-androidbind.aar` — **a build artifact, not source. Not
  committed to SVN.** Regenerate it whenever `pkg/mobileclient/androidbind`
  (or anything it depends on) changes; see below.

## Regenerating the AAR

From the repository root, using a scratch Go module that references this
checkout via `replace` (never add `golang.org/x/mobile` to the main
`go.mod` — see the project memory/notes on why):

```sh
# one-time setup of the scratch module, if you don't already have one
mkdir -p ~/androidbind-smoke/smoke && cd ~/androidbind-smoke
cat > go.mod <<'EOF'
module filees-androidbind

go 1.25.0

replace filees => $HOME/Filees-Android
EOF
go mod edit -require=filees@v0.0.0-00010101000000-000000000000
go get -tool golang.org/x/mobile/cmd/gobind@latest
go get -tool golang.org/x/mobile/cmd/gomobile@latest

# rebuild
export ANDROID_HOME=$HOME/Android/Sdk
export ANDROID_NDK_HOME=$HOME/Android/Sdk/ndk/27.2.12479018
export GOFLAGS=-buildvcs=false
cd ~/androidbind-smoke
gomobile bind -target=android -androidapi 24 -o filees-androidbind.aar filees/pkg/mobileclient/androidbind
cp filees-androidbind.aar $HOME/Filees-Android/android/app/libs/
```

## Building

```sh
cd android
./gradlew assembleDebug
```

`local.properties` (machine-specific `sdk.dir`, not committed) must point at
a working Android SDK; see the project memory for how the SDK/NDK/gomobile
toolchain was set up on this box.

## Running on the emulator

The `medium_phone` AVD used to segfault on startup regardless of GPU backend.
Root cause: SELinux (Enforcing) was denying `execheap` to the emulator's
`RenderThread` (SwiftShader's JIT needs to mprotect its heap-allocated code
buffer executable). Fixed with:

```sh
sudo setsebool -P selinuxuser_execheap on
```

(reversible with `... off`). After that fix the AVD boots cleanly
(`INFO | Boot completed in ...`), `adb devices` shows it as `device`, and
`adb install app-debug.apk` + `adb shell pm list packages` /
`dumpsys package net.filees.mobile` confirm the app and its
`ManifestCacheProvider` install and register correctly. `adb shell content
query` against it correctly gets a `SecurityException` — that's the intended
`exported=false` boundary working, not a bug; the `content` CLI always goes
through the "external" provider-access path (which even `run-as` can't
satisfy), so exercising the provider's actual query logic needs an in-app
caller (instrumented test or an Activity) once one exists.

## Verified real end-to-end on-device (2026-07-22)

With `MainActivity`, the whole chain has been driven for real from the
emulator, not just built: entered `10.0.2.2:2222` (the Android emulator's
alias for the host's loopback — **not** `127.0.0.1`, which is the emulator's
own loopback) and the lab VM's pinned host key, tapped "Aktywuj klienta na
nowym serwerze…", got the device's freshly generated Ed25519 public key back
in the UI. First "Odśwież" attempt correctly surfaced
`sshtransport: handshake: ... "Too many authentication failures"` — the
freshly generated device key wasn't registered yet, exactly as it shouldn't
be. After adding that device's key to the VM's `_filees-mobile` authorized_keys
and a grant entry (same additive process as the Etap 4b test device), a
second "Odśwież" returned the real manifest: `revision=2 generation=1`,
`photos/a.jpg (6 B)`, `photos/e2e-real.txt (21 B)` — matching the file
uploaded during the Etap 4b Go-only end-to-end test exactly.

Practical notes from driving the UI over `adb` for this test:
- Use `adb shell uiautomator dump` to get exact view `bounds` rather than
  guessing tap coordinates from a screenshot — screenshots don't reliably
  tell you where a view boundary actually is once layout has reflowed (e.g.
  a multi-line field growing).
- Loop many `input keyevent`/`input tap` calls **inside one `adb shell`
  invocation** (`adb shell "for i in ...; do input keyevent 67; done"`), not
  as separate `adb shell` processes per keystroke — the latter is slow
  enough under load that keystrokes get dropped or land after the field
  loses focus.
- `adb shell input text` truncates long strings unpredictably; split long
  text (like a pasted SSH key) into several `input text` calls.
- A fresh `google_apis_playstore` system image is very noisy on first boot
  (Play Store/GMS background sync, Chimera module downloads) and will throw
  a string of unrelated `*isn't responding*` ANRs for `system_server`,
  the launcher, and SystemUI in the first few minutes — none of that is
  about this app; wait it out rather than debugging it.

## Capture stabilization — 2026-09-27

CaptureTransfers is the common foreground/watch spool-and-send path. SAF and
ZIP data stream into the durable Go queue before network I/O. Sources + ID
survive lost ACKs/restarts. Delivered/deduplicated are the only success states;
conflict/parked/uncertain remain queued. A process-wide coordinator and the
Go store lock serialize work; cancellation closes active I/O. WorkManager
appends the next tick, and successful background sends stay silent.

The frame protocol does not resume at byte offsets: retry sends the same
whole pack under the same ID. The client batches at 32 MiB / 1000 files and
accepts at most 2 GiB per file/unpacked pack; the authoritative server limit
is 5000 entries / 2 GiB, with 16 MiB ZIP overhead. Unknown SAF sizes are
measured, never treated as permission to buffer an unlimited file in RAM.

Instrumentation has no JUnit dependency: `assembleDebugAndroidTest`, then
`adb shell am instrument -w net.filees.mobile.test/net.filees.mobile.CaptureInstrumentation`.
Its provider is in the **test APK only**. The optional SSH/SVN fixture is
```sh
FILEES_CAPTURE_EMULATOR_FIXTURE=/tmp/<new-dir> go test ./pkg/mobileclient/androidbind \
  -run '^TestCaptureEmulatorBackend$' -v -timeout 22m
```
Pass its endpoint.json host_key as an instrumentation argument; create
`<new-dir>/done` afterward. For the separate process-restart test, use a new
fixture and run instrumentation with `-e process_phase prepare`, force-stop
`net.filees.mobile`, then run `-e process_phase resume` with the same host key.
Instrumentation waits for Application startup before creating its SAF payloads.
See implementation notes (not distributed) for the exact run.

## 0.1.18 UI and release identity (2026-09-27)

The header reports watched-upload state rather than an empty decisions count.
Each watched folder opens a status card with its latest scan, retained queue
(up to 200 entries) and recent journal (30 transitions). Complete refers to
the selected tracking scope and the last successful scan; an empty queue
before scanning is not proof of completion. Advanced settings holds desktop
activation, server management and emergency JSON pairing. The lock prompt is
simply “Unlock the app”.

About contains an opt-in automatic-update switch, off by default. WorkManager
checks the signed channel roughly daily when connected and storage is not low,
verifies/downloads the APK, and announces a new prepared update once. Installation
still requires the Android confirmation. Manual checking remains available.
Disabling stops future background work; an already prepared APK remains available
for manual installation. No success notification is emitted for watched uploads.

Gradle reads the root VERSION shared with desktop, and svnversion supplies
versionName = VERSION+rREV and versionCode = REV (advancing from the former
manual code 55). Dirty/mixed developer copies show the SVN suffix; release
builds require a clean uniform WC. Rebuild the Go AAR from the same source
before building the APK. After source commits and svn up, build the release
APK; a newer source commit requires rebuilding it before staging.
`tools/prepare-android-release.sh` uses `tools/check-android-apk.sh` to inspect
the binary manifest with SDK aapt2, matching package, version and revision/code,
and refuses a debug APK before mutating FILEES-BIN. Set ANDROID_HOME or AAPT2.
The signed manifest keeps its existing VERSION.REV format and schema.
Developer builds/tests do not promote any release channel.
