#!/bin/sh
set -eu

if [ "$(uname -s)" != OpenBSD ] || [ "$(id -u)" -ne 0 ]; then
	echo "install-ssh.sh requires root on OpenBSD" >&2
	exit 1
fi

bundle=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
state_user=_filees-state

if ! id "$state_user" >/dev/null 2>&1; then
	useradd -c "FileES state owner" -d /var/empty -s /sbin/nologin "$state_user"
fi

install -o root -g wheel -m 644 "$bundle/share/filees/openbsd/filees-tunnel.login.conf" /etc/login.conf.d/filees-tunnel
cap_mkdb /etc/login.conf.d/filees-tunnel

if ! id _filees-onboard >/dev/null 2>&1; then
	useradd -c "FileES SSH onboarding" -d /var/empty -s /bin/sh _filees-onboard
fi
if ! id _filees-tunnel >/dev/null 2>&1; then
	useradd -c "FileES SSH tunnel" -L filees-tunnel -d /var/empty -s /bin/sh _filees-tunnel
fi
# A '*' password field makes BSD Authentication reject the account before a
# local style can run. Install an unknowable random hash; sshd still disables
# both password and public-key authentication for this account.
password_hash=$(openssl rand -hex 32 | encrypt -b 12)
usermod -p "$password_hash" _filees-tunnel
unset password_hash
install -o "$state_user" -g auth -m 4550 "$bundle/bin/filees-ssh-auth" /usr/libexec/auth/login_-filees
install -o "$state_user" -g wheel -m 4511 "$bundle/bin/filees-onboard" /usr/local/libexec/filees/filees-onboard
install -o "$state_user" -g wheel -m 4511 "$bundle/bin/filees-entry" /usr/local/libexec/filees/filees-entry

install -o root -g wheel -m 644 "$bundle/share/filees/openbsd/bootstrap_authorized_keys" /etc/ssh/filees_bootstrap_authorized_keys
install -d -o root -g wheel -m 755 /etc/ssh/sshd_config.d
install -o root -g wheel -m 644 "$bundle/share/filees/openbsd/filees.conf" /etc/ssh/sshd_config.d/filees.conf
if ! grep -q '^Include /etc/ssh/sshd_config.d/\*.conf$' /etc/ssh/sshd_config; then
	if [ ! -e /etc/ssh/sshd_config.filees-before-s2 ]; then
		cp -p /etc/ssh/sshd_config /etc/ssh/sshd_config.filees-before-s2
	fi
	printf '\nMatch all\nInclude /etc/ssh/sshd_config.d/*.conf\n' >>/etc/ssh/sshd_config
fi

chown "$state_user":wheel /var/filees
chmod 700 /var/filees
chown -R "$state_user":wheel /var/filees/onboarding
chmod 700 /var/filees/onboarding /var/filees/onboarding/tickets /var/filees/onboarding/operations /var/filees/onboarding/audit
chown "$state_user":wheel /etc/filees
chmod 700 /etc/filees
chown "$state_user":wheel /etc/filees/server.json /etc/filees/otp.pepper
chmod 600 /etc/filees/server.json /etc/filees/otp.pepper

sshd -t
rcctl reload sshd
echo "FileES S2 SSH entries installed; no service or listener was added."
