#!/bin/sh
# Acceptance of pam_filees_tunnel.so through real libpam. Must run as root on
# Linux (the module drops to the state user before starting the helper).
# Optional: REAL_HELPER=/path/to/filees-ssh-auth checks the wire format against
# the real Go helper.
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
chmod 755 "$work"
sh "$here/../build.sh" "$work/pam_filees_tunnel.so" >/dev/null
${CC:-cc} -O2 -Wall -Wextra -Werror -o "$work/harness" "$here/harness.c" -lpam
fails=0
check() { # name expected-exit actual-exit
	if [ "$2" = "$3" ]; then echo "PASS $1"; else echo "FAIL $1 (exit $3, want $2)"; fails=$((fails+1)); fi
}
setup() { # mode -> writes helper and pam.d for it
	sed -e "s|@MODE@|$1|" -e "s|@WHO@|$work/who|" -e "s|@SEEN@|$work/seen|" "$here/fake-helper.sh" > "$work/helper"
	chmod 755 "$work/helper"
	rm -f "$work/who" "$work/seen" "$work/prompt"
	: > "$work/who"; : > "$work/seen"; chmod 666 "$work/who" "$work/seen"
	mkdir -p "$work/pam.d"
	printf 'auth requisite %s helper=%s state_user=nobody\naccount requisite %s\n' \
		"$work/pam_filees_tunnel.so" "${2:-$work/helper}" "$work/pam_filees_tunnel.so" > "$work/pam.d/filees-tunnel"
}
run() { set +e; "$work/harness" "$work/pam.d" "$@"; rc=$?; set -e; }

setup ok
run _filees-tunnel good-otp "$work/prompt"; check "correct answer is accepted" 0 $rc
[ "$(cat "$work/who")" = nobody ] && echo "PASS helper ran as the state user, not root" || { echo "FAIL helper ran as $(cat "$work/who")"; fails=$((fails+1)); }
[ "$(cat "$work/prompt")" = "$(printf 'FileES authentication nonce: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:\n')" ] && echo "PASS client sees the helper's challenge as the prompt" || { echo "FAIL prompt was: $(cat "$work/prompt")"; fails=$((fails+1)); }
[ "$(sed -n '3p' "$work/seen")" = good-otp ] && echo "PASS helper received challenge and answer as two fields" || { echo "FAIL helper saw: $(cat "$work/seen")"; fails=$((fails+1)); }

setup ok
run _filees-tunnel wrong-otp; check "wrong answer is refused" 1 $rc
run _filees-tunnel ""; check "empty answer is refused" 1 $rc
run _filees-tunnel "$(head -c 1500 /dev/zero | tr '\0' x)"; check "oversized answer is refused" 1 $rc
setup ok
run root good-otp; check "any other user is refused" 1 $rc
[ ! -s "$work/seen" ] && echo "PASS helper is not consulted for another user" || { echo "FAIL helper was consulted for root"; fails=$((fails+1)); }

setup chatty
run _filees-tunnel good-otp; check "authorize followed by extra output is refused" 1 $rc
setup nochallenge
run _filees-tunnel good-otp; check "helper without a challenge is unavailable, not success" 2 $rc
setup ok /nonexistent/helper
run _filees-tunnel good-otp; check "missing helper is unavailable, not success" 2 $rc

if [ -n "${REAL_HELPER:-}" ]; then
	setup ok "$REAL_HELPER"
	run _filees-tunnel 000000 "$work/prompt"; check "real helper: bogus OTP is not accepted" 1 $rc || true
	case "$(cat "$work/prompt" 2>/dev/null)" in
	"FileES authentication nonce: "*:) echo "PASS real helper: challenge decoded into the prompt" ;;
	*) echo "FAIL real helper prompt: $(cat "$work/prompt" 2>/dev/null)"; fails=$((fails+1)) ;;
	esac
fi
[ "$fails" -eq 0 ] && echo "ALL PASS" || { echo "$fails FAILED"; exit 1; }
