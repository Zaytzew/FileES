#!/bin/sh
set -eu

# Local, credential-free recovery smoke test. It is intentionally opt-in and
# uses svnadmin/file://, so CI never mutates a shared repository.
command -v svnadmin >/dev/null
command -v svn >/dev/null
command -v go >/dev/null

root=$(mktemp -d "${TMPDIR:-/tmp}/filees-svn-recovery.XXXXXX")
daemon_pid=""
cleanup() {
	if test -n "$daemon_pid"; then
		kill -KILL "$daemon_pid" 2>/dev/null || true
	fi
	rm -rf "$root"
}
trap cleanup EXIT INT TERM

repo="$root/repo"
wc="$root/wc"
config="$root/config.json"
runtime="$root/runtime"
daemon="$root/filees"
svnadmin create "$repo"
url="file://$repo/project"
svn mkdir "$url" -m "smoke: initialize" >/dev/null
svn checkout "$url" "$wc" >/dev/null
mkdir -p "$wc/smoke"
printf '%s\n' 'baseline' > "$wc/smoke/anchor.txt"
svn add "$wc/smoke"
svn commit "$wc/smoke" -m "smoke: baseline" >/dev/null

go build -buildvcs=false -o "$daemon" ./cmd/filees
cat >"$config" <<EOF
{
  "server_id": "local-recovery-smoke",
  "server_display_name": "local recovery smoke",
  "client_role": "normal",
  "transport": {
    "identity_file": "$root/test-id",
    "known_hosts": "$root/test-known-hosts"
  },
  "repositories": [{
    "id": "local-smoke",
    "repo_url": "svn+ssh://_filees-client@smoke.invalid/project",
    "local_path": "$wc",
	"access": "rw",
    "commit_interval": "1h",
    "watch_interval": "2s",
    "poll_interval": "1h",
    "max_batch_files": 1,
    "max_batch_mib": 1,
    "backlog_flush_mib": 100,
    "shutdown_commit_timeout": "30s"
  }]
}
EOF

XDG_RUNTIME_DIR="$runtime" FILEES_LOG=error "$daemon" daemon --config "$config" >/dev/null 2>&1 &
daemon_pid=$!
manifest="$wc/.filees/state/manifest.json"
cache="$wc/.filees/commit_cache/cache.json"
for _ in $(seq 1 30); do test -f "$manifest" && break; sleep 1; done
test -f "$manifest"

printf '%s\n' 'changed before crash' > "$wc/smoke/anchor.txt"
printf '%s\n' 'new before crash' > "$wc/smoke/new.txt"
for _ in $(seq 1 30); do test -f "$cache" && test "$(wc -c <"$cache")" -gt 2 && break; sleep 1; done
test -f "$cache"

kill -KILL "$daemon_pid"
daemon_pid=""

XDG_RUNTIME_DIR="$runtime" FILEES_LOG=error "$daemon" daemon --config "$config" >/dev/null 2>&1 &
daemon_pid=$!
sleep 2
kill -INT "$daemon_pid"
for _ in $(seq 1 30); do kill -0 "$daemon_pid" 2>/dev/null || break; sleep 1; done
daemon_pid=""

test "$(wc -c <"$cache")" -le 3
test -z "$(svn status "$wc/smoke")"
test "$(svn log -q "$url" | grep -c 'r')" -ge 3
echo "svn recovery smoke: PASS"
