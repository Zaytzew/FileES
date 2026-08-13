#!/bin/sh
set -eu

if [ "$(uname -s)" != OpenBSD ] || [ "$(id -u)" -ne 0 ]; then
	echo "install-ssh.sh requires root on OpenBSD" >&2
	exit 1
fi

bundle=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
state_user=_filees-state
client_access_group=_filees-access
public_access_group=_filees-public

if ! grep -q "^${client_access_group}:" /etc/group; then
	groupadd "$client_access_group"
fi
if ! grep -q "^${public_access_group}:" /etc/group; then
	groupadd "$public_access_group"
fi

if ! id "$state_user" >/dev/null 2>&1; then
	useradd -c "FileES state owner" -d /var/empty -s /sbin/nologin "$state_user"
fi
if ! id _filees-links >/dev/null 2>&1; then
	useradd -c "FileES public links" -d /var/empty -s /sbin/nologin _filees-links
fi
usermod -G "$public_access_group" "$state_user"
usermod -G "$public_access_group,www" _filees-links

install -o root -g wheel -m 644 "$bundle/share/filees/openbsd/filees-tunnel.login.conf" /etc/login.conf.d/filees-tunnel
cap_mkdb /etc/login.conf.d/filees-tunnel

if ! id _filees-onboard >/dev/null 2>&1; then
	useradd -c "FileES SSH onboarding" -d /var/empty -s /bin/sh _filees-onboard
fi
if ! id _filees-tunnel >/dev/null 2>&1; then
	useradd -c "FileES SSH tunnel" -L filees-tunnel -d /var/empty -s /bin/sh _filees-tunnel
fi
if ! id _filees-client >/dev/null 2>&1; then
	useradd -c "FileES active SVN client" -d /var/empty -s /bin/sh _filees-client
fi
if ! id _filees-data >/dev/null 2>&1; then
	useradd -c "FileES data repository client" -d /var/empty -s /bin/sh _filees-data
fi
if ! id _filees-recovery >/dev/null 2>&1; then
	useradd -c "FileES recovery download" -d /var/empty -s /bin/sh _filees-recovery
fi
if ! id _filees-mobile >/dev/null 2>&1; then
	useradd -c "FileES mobile client" -d /var/empty -s /bin/sh _filees-mobile
fi
# A '*' password field makes BSD Authentication reject the account before a
# local style can run. Install an unknowable random hash; sshd still disables
# password authentication. Public-key policy remains explicit per Match block.
password_hash=$(openssl rand -hex 32 | encrypt -b 12)
usermod -p "$password_hash" _filees-tunnel
usermod -p "$password_hash" _filees-client
usermod -p "$password_hash" _filees-data
usermod -p "$password_hash" _filees-recovery
usermod -p "$password_hash" _filees-mobile
usermod -G "$client_access_group" _filees-client
usermod -G "$client_access_group" _filees-data
usermod -G "$client_access_group" _filees-mobile
unset password_hash
# Reinstall the administrative tools here as well so the complete two-stage
# bootstrap establishes explicit OpenBSD ownership for every adopted target.
install -o root -g wheel -m 0555 "$bundle/bin/filees-admin" /usr/local/sbin/filees-admin
install -o root -g wheel -m 0555 "$bundle/bin/filees-operation" /usr/local/sbin/filees-operation
install -o root -g wheel -m 0555 "$bundle/bin/filees-install" /usr/local/sbin/filees-install
install -o root -g wheel -m 0555 "$bundle/bin/filees-rotate" /usr/local/sbin/filees-rotate
install -o "$state_user" -g auth -m 4550 "$bundle/bin/filees-ssh-auth" /usr/libexec/auth/login_-filees
# The dispatcher is the sole set-id boundary. Its two ordinary child images
# inherit the effective state UID; pledge execpromises reject set-id children.
install -o root -g wheel -m 0555 "$bundle/bin/filees-onboard" /usr/local/libexec/filees/filees-onboard
install -o "$state_user" -g wheel -m 4511 "$bundle/bin/filees-bootstrap-entry" /usr/local/libexec/filees/filees-bootstrap-entry
# pledge(2) rejects exec of a set-id image after execpromises have been set.
# Keep the privilege boundary on the tiny forced-command dispatcher; the
# ordinary worker image inherits its effective state UID across exec.
install -o "$state_user" -g wheel -m 4511 "$bundle/bin/filees-entry" /usr/local/libexec/filees/filees-entry
install -o root -g wheel -m 0555 "$bundle/bin/filees-worker" /usr/local/libexec/filees/filees-worker
install -o root -g wheel -m 0555 "$bundle/bin/filees-mail" /usr/local/libexec/filees/filees-mail
install -o root -g wheel -m 0555 "$bundle/bin/filees-public-authority" /usr/local/libexec/filees/filees-public-authority
install -o root -g wheel -m 0555 "$bundle/bin/filees-links" /usr/local/libexec/filees/filees-links
install -o "$state_user" -g "$client_access_group" -m 4550 "$bundle/bin/filees-client-entry" /usr/local/libexec/filees/filees-client-entry
install -o "$state_user" -g "$client_access_group" -m 4550 "$bundle/bin/filees-mobile-v1" /usr/local/libexec/filees/filees-mobile-v1
install -o "$state_user" -g _filees-recovery -m 4550 "$bundle/bin/filees-recovery-entry" /usr/local/libexec/filees/filees-recovery-entry
# OpenSSH requires every AuthorizedKeysCommand component to be root-owned and
# not writable by group/other. The session entry remains the separate set-id
# boundary; this second image executes only the read-only "authorize" branch.
install -o root -g wheel -m 0555 "$bundle/bin/filees-recovery-entry" /usr/local/libexec/filees/filees-recovery-authorize

install -o root -g wheel -m 644 "$bundle/share/filees/openbsd/bootstrap_authorized_keys" /etc/ssh/filees_bootstrap_authorized_keys
install -d -o root -g wheel -m 755 /etc/ssh/sshd_config.d
install -o root -g wheel -m 644 "$bundle/share/filees/openbsd/filees.conf" /etc/ssh/sshd_config.d/filees.conf
install -o root -g wheel -m 0555 "$bundle/share/filees/openbsd/filees_public_authority" /etc/rc.d/filees_public_authority
install -o root -g wheel -m 0555 "$bundle/share/filees/openbsd/filees_links" /etc/rc.d/filees_links
install -o root -g wheel -m 0644 "$bundle/share/filees/openbsd/public-links.httpd.conf" /etc/examples/filees-public-links.httpd.conf

# The ports build of svnserve probes this fixed SASL configuration directory.
# Keep it present so filees-client-entry can unveil the exact path, not /etc.
install -d -o root -g wheel -m 755 /etc/sasl2 /etc/subversion

install -d -o "$state_user" -g wheel -m 700 /var/filees/activation /var/filees/activation/records /var/filees/activation/proofs /var/filees/sessions
install -d -o "$state_user" -g wheel -m 700 /var/filees/repositories /var/filees/repository-operations
install -d -o "$state_user" -g wheel -m 700 /var/filees-mobile /var/filees-mobile/ledger
install -d -o "$state_user" -g wheel -m 700 /var/filees/repository-operations/public-shares
install -d -o "$state_user" -g wheel -m 700 /var/tmp/filees-public-share-authority
install -d -o _filees-links -g wheel -m 700 /var/tmp/filees-public-shares-cache
install -d -o "$state_user" -g "$public_access_group" -m 750 /var/run/filees
install -d -o _filees-links -g www -m 750 /var/www/run/filees
if [ ! -e /var/filees/activation/authorized_keys ]; then
	install -o "$state_user" -g wheel -m 600 /dev/null /var/filees/activation/authorized_keys
fi
if [ ! -e /var/filees/activation/repositories.authz ]; then
	install -o "$state_user" -g wheel -m 600 /dev/null /var/filees/activation/repositories.authz
fi
if [ ! -e /var/filees/activation/service.authz ]; then
	install -o "$state_user" -g wheel -m 600 /dev/null /var/filees/activation/service.authz
fi
if [ ! -d /var/filees/service-repo ]; then
	/usr/local/bin/svnadmin create /var/filees/service-repo
	/usr/local/bin/svn mkdir --non-interactive --no-auth-cache -m "filees: initialize proof" file:///var/filees/service-repo/proof
fi
if [ ! -d /var/filees/service-wc/.svn ]; then
	/usr/local/bin/svn checkout --non-interactive --no-auth-cache file:///var/filees/service-repo /var/filees/service-wc
fi
cat > /var/filees/service-repo/conf/svnserve.conf <<'EOF'
[general]
anon-access = none
auth-access = read
authz-db = /var/filees/activation/service.authz
EOF
chown -R "$state_user":wheel /var/filees/service-repo /var/filees/service-wc /var/filees/activation
chmod 700 /var/filees/service-repo /var/filees/service-wc /var/filees/activation
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
chown "$state_user":"$public_access_group" /etc/filees
chmod 750 /etc/filees
chown "$state_user":wheel /etc/filees/server.json /etc/filees/otp.pepper /etc/filees/worker_ed25519
chmod 600 /etc/filees/server.json /etc/filees/otp.pepper /etc/filees/worker_ed25519
chown "$state_user":wheel /etc/filees/public-share-frost.key
chmod 600 /etc/filees/public-share-frost.key
chown _filees-links:wheel /etc/filees/public-links.json /etc/filees/public-share-visit.key
chmod 600 /etc/filees/public-links.json /etc/filees/public-share-visit.key
chown root:wheel /etc/filees/worker_ed25519.pub
chmod 644 /etc/filees/worker_ed25519.pub

sshd -t
rcctl reload sshd
echo "FileES SSH entries, Public Shares users, binaries and disabled rc.d scripts installed; no Public Shares listener was started."
