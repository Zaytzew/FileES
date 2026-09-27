#!/bin/sh
# Verify the actual APK identity before staging an immutable Android release.
set -eu
apk=${1:?APK path required}
allow_debug=${2:-}
expected_version=${EXPECTED_ANDROID_VERSION:?expected versionName required}
expected_code=${EXPECTED_ANDROID_CODE:?expected versionCode required}
aapt=${AAPT2:-}
if [ -z "$aapt" ]; then
  aapt=$(command -v aapt2 || true)
fi
if [ -z "$aapt" ] && [ -n "${ANDROID_HOME:-}" ]; then
  for candidate in "$ANDROID_HOME"/build-tools/*/aapt2; do
    [ ! -x "$candidate" ] || aapt=$candidate
  done
fi
[ -n "$aapt" ] || { echo 'Android SDK aapt2 required (AAPT2 or ANDROID_HOME)' >&2; exit 1; }
metadata=$("$aapt" dump badging "$apk")
package=$(printf '%s\n' "$metadata" | sed -n "s/^package: name='\([^']*\)'.*/\1/p")
version=$(printf '%s\n' "$metadata" | sed -n "s/^package:.* versionName='\([^']*\)'.*/\1/p")
code=$(printf '%s\n' "$metadata" | sed -n "s/^package:.* versionCode='\([^']*\)'.*/\1/p")
[ "$package" = net.filees.mobile ] && [ "$version" = "$expected_version" ] && [ "$code" = "$expected_code" ] || {
  echo "APK identity mismatch: $package $version code=$code; expected net.filees.mobile $expected_version code=$expected_code" >&2
  exit 1
}
if [ "$allow_debug" != --allow-debug ]; then
  case "$metadata" in *application-debuggable*) echo 'Debug APK cannot be staged for publication' >&2; exit 1 ;; esac
fi
printf 'APK identity verified: %s (versionCode %s)\n' "$version" "$code"
