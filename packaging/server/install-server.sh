#!/bin/sh
set -eu

bundle=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
prefix=${PREFIX:-/usr/local}
sysconfdir=${SYSCONFDIR:-/etc/filees}
statedir=${STATEDIR:-/var/filees/onboarding}

install -d -m 755 "$prefix/sbin" "$prefix/libexec/filees"
install -m 755 "$bundle/bin/filees-admin" "$prefix/sbin/filees-admin"
install -m 755 "$bundle/bin/filees-operation" "$prefix/sbin/filees-operation"
install -m 755 "$bundle/bin/filees-onboard" "$prefix/libexec/filees/filees-onboard"
install -m 755 "$bundle/bin/filees-bootstrap-entry" "$prefix/libexec/filees/filees-bootstrap-entry"
install -m 755 "$bundle/bin/filees-mail" "$prefix/libexec/filees/filees-mail"
install -m 755 "$bundle/bin/filees-entry" "$prefix/libexec/filees/filees-entry"
install -m 755 "$bundle/bin/filees-worker" "$prefix/libexec/filees/filees-worker"
install -m 755 "$bundle/bin/filees-client-entry" "$prefix/libexec/filees/filees-client-entry"

install -d -m 700 "$sysconfdir"
if [ ! -e "$sysconfdir/server.json" ]; then
	install -m 600 "$bundle/share/filees/server.example.json" "$sysconfdir/server.json"
fi
if [ ! -e "$sysconfdir/otp.pepper" ]; then
	umask 077
	openssl rand -base64 32 >"$sysconfdir/otp.pepper"
fi
if [ ! -e "$sysconfdir/worker_ed25519" ]; then
	umask 077
	ssh-keygen -q -t ed25519 -N '' -C filees-worker-v1 -f "$sysconfdir/worker_ed25519"
fi

install -d -m 700 "$statedir" "$statedir/tickets" "$statedir/operations" "$statedir/audit"
if [ ! -e "$statedir/.toolchain.lock" ]; then
	install -m 600 /dev/null "$statedir/.toolchain.lock"
fi
install -d -m 700 /var/filees/activation /var/filees/activation/records /var/filees/activation/proofs

echo "FileES server tools installed. Edit $sysconfdir/server.json before use."
echo "No daemon or rc.d service was installed."
echo "On OpenBSD, review and run openbsd/install-ssh.sh to enable the system-sshd entries."
