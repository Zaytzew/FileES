# FileES Android client — Etap 6

This is the Kotlin/Gradle side of the mobile client from
`concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md`. The Go core (protocol,
worker-side dispatcher, local store, upload queue, embedded SSH transport)
lives in `pkg/mobile/v1`, `internal/mobileworker` and `pkg/mobileclient`; this
directory is only the Kotlin shell around the `pkg/mobileclient/androidbind`
gomobile binding — see `SESSION_HANDOFF.md` §17 and
`reports/ANDROID_MOBILE_CLIENT_IMPLEMENTATION_REPORT.md` for how that side was
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

replace filees => /home/acme/Filees-Android
EOF
go mod edit -require=filees@v0.0.0-00010101000000-000000000000
go get -tool golang.org/x/mobile/cmd/gobind@latest
go get -tool golang.org/x/mobile/cmd/gomobile@latest

# rebuild
export ANDROID_HOME=/home/acme/Android/Sdk
export ANDROID_NDK_HOME=/home/acme/Android/Sdk/ndk/27.2.12479018
export GOFLAGS=-buildvcs=false
cd ~/androidbind-smoke
gomobile bind -target=android -androidapi 24 -o filees-androidbind.aar filees/pkg/mobileclient/androidbind
cp filees-androidbind.aar /home/acme/Filees-Android/android/app/libs/
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
