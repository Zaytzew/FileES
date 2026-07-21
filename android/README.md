# FileES Android client — Etap 6 skeleton

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
  (`androidbind.Store`). No Activity yet; this is the first Etap 6 slice from
  `SESSION_HANDOFF.md` §17's resumption list.
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

## Known gap: the emulator

The `medium_phone` AVD segfaults on startup in the sandbox this was built in,
regardless of GPU backend — see the test report for details. This skeleton
has been built and its Gradle config validated, but **not yet run on a
device or working emulator**. That's the next concrete blocker for Etap 6,
independent of any more Kotlin code being written.
