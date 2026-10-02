#!/bin/sh
# Second stage of a FileES server installation on Linux: system accounts,
# binaries with their set-id boundaries, state directories, the service
# repository and the sshd entries. Run install-server.sh first.
#
# Linux is the build without a sandbox: no pledge, no unveil, and nothing here
# adds a firewall, a web server or service isolation (owner, 2026-10-01).
# Public shares are not configured; see share/filees/linux for an example.
set -eu

if [ "$(uname -s)" != Linux ] || [ "$(id -u)" -ne 0 ]; then
	echo "install-ssh.sh requires root on Linux" >&2
	exit 1
fi

bundle=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
state_user=_filees-state
client_access_group=_filees-access
public_access_group=_filees-public
libexec=/usr/local/libexec/filees
public_downloads_dir=${PUBLIC_DOWNLOADS_DIR:-/var/filees-downloads}
public_authority_staging_root=${PUBLIC_AUTHORITY_STAGING_ROOT:-$public_downloads_dir/authority}
public_links_cache_root=${PUBLIC_LINKS_CACHE_ROOT:-$public_downloads_dir/cache}
public_upload_intake_root=${PUBLIC_UPLOAD_INTAKE_ROOT:-$public_downloads_dir/intake}
mobile_temp_root=${MOBILE_TEMP_ROOT:-/var/filees-mobile/tmp}
case "$mobile_temp_root" in
    /|/tmp|/var|/home|/var/tmp) echo "Mobile uploads require a dedicated temporary directory" >&2; exit 1 ;;
    /*) ;;
    *) echo "MOBILE_TEMP_ROOT must be absolute" >&2; exit 1 ;;
esac
if [ -L "$mobile_temp_root" ]; then
    echo "MOBILE_TEMP_ROOT must not be a symlink" >&2; exit 1
fi
for public_path in "$public_downloads_dir" "$public_authority_staging_root" "$public_links_cache_root" "$public_upload_intake_root"; do
	case "$public_path" in
		/|/tmp|/tmp/*|/var/tmp|/var/tmp/*) echo "Public downloads require dedicated persistent directories" >&2; exit 1 ;;
		/*) ;;
		*) echo "Public downloads paths must be absolute" >&2; exit 1 ;;
	esac
done

need() {
	command -v "$1" >/dev/null 2>&1 || { echo "install-ssh.sh: $1 is required${2:+ ($2)}" >&2; exit 1; }
}
need sshd "OpenSSH server"
need svnadmin "Subversion"
need svn "Subversion"
need svnserve "Subversion"
need useradd
need openssl
svnadmin=$(command -v svnadmin)
svn=$(command -v svn)

# The tunnel account's own PAM service is chosen inside a Match block
# (PAMServiceName), which sshd understands from OpenSSH 9.8.
ssh_version=$(sshd -V 2>&1 | sed -n 's/^OpenSSH_\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p' | head -n 1)
[ -n "$ssh_version" ] || { echo "install-ssh.sh: cannot read the OpenSSH version" >&2; exit 1; }
set -- $ssh_version
if [ "$1" -lt 9 ] || { [ "$1" -eq 9 ] && [ "$2" -lt 8 ]; }; then
	echo "install-ssh.sh: OpenSSH 9.8 or newer is required (found $1.$2)" >&2
	exit 1
fi
if ! sshd -T 2>/dev/null | grep -qi '^usepam yes$'; then
	echo "install-ssh.sh: sshd must run with UsePAM yes (the tunnel account authenticates through PAM)" >&2
	exit 1
fi

for required in "$bundle/lib/pam_filees_tunnel.so" "$bundle/share/filees/linux/filees.conf" \
	"$bundle/share/filees/linux/filees.cil" \
	"$bundle/share/filees/linux/filees-tunnel.pam" "$bundle/share/filees/linux/bootstrap_authorized_keys" \
	/etc/filees/server.json /etc/filees/otp.pepper /etc/filees/worker_ed25519 /var/filees/onboarding; do
	[ -e "$required" ] || { echo "install-ssh.sh: $required is missing; run install-server.sh from a Linux bundle first" >&2; exit 1; }
done

group_exists() { getent group "$1" >/dev/null 2>&1; }
group_exists "$client_access_group" || groupadd --system "$client_access_group"
group_exists "$public_access_group" || groupadd --system "$public_access_group"

# Home of every FileES account: an empty directory nobody but root can write.
install -d -o root -g root -m 755 /var/empty

# Service accounts never log in: no shell. SSH entry accounts need a shell
# only because sshd starts ForceCommand through it.
add_account() { # name comment shell
	if ! id "$1" >/dev/null 2>&1; then
		useradd --system --user-group --no-create-home --home-dir /var/empty \
			--shell "$3" --comment "$2" "$1"
	fi
}
nologin=$(command -v nologin || echo /usr/sbin/nologin)
add_account "$state_user" "FileES state owner" "$nologin"
add_account _filees-links "FileES public links" "$nologin"
add_account _filees-onboard "FileES SSH onboarding" /bin/sh
add_account _filees-tunnel "FileES SSH tunnel" /bin/sh
add_account _filees-client "FileES active SVN client" /bin/sh
add_account _filees-data "FileES data repository client" /bin/sh
add_account _filees-recovery "FileES recovery download" /bin/sh
add_account _filees-mobile "FileES mobile client" /bin/sh
usermod -G "$public_access_group" "$state_user"
usermod -G "$public_access_group" _filees-links
# '*' is "no password can ever match", not "locked": sshd refuses a locked
# ('!') account even for a key. Password authentication stays disabled for
# every one of these accounts in sshd_config.d/filees.conf.
for account in _filees-onboard _filees-tunnel _filees-client _filees-data _filees-recovery _filees-mobile; do
	usermod -p '*' "$account"
done
usermod -G "$client_access_group" _filees-client
usermod -G "$client_access_group" _filees-data
usermod -G "$client_access_group" _filees-mobile
# filees-recovery-entry is 4550 state:_filees-recovery, so only that group may
# execute it; the account's primary group must be exactly that one.
usermod -g _filees-recovery _filees-recovery

install -d -o root -g root -m 755 /usr/local/sbin /usr/local/libexec "$libexec"
install -o root -g root -m 0555 "$bundle/bin/filees-admin" /usr/local/sbin/filees-admin
install -o root -g root -m 0555 "$bundle/bin/filees-operation" /usr/local/sbin/filees-operation
install -o root -g root -m 0555 "$bundle/bin/filees-install" /usr/local/sbin/filees-install
install -o root -g root -m 0555 "$bundle/bin/filees-rotate" /usr/local/sbin/filees-rotate
# The PAM module switches to the state user before it starts the helper, so
# the helper needs no set-id bit here (OpenBSD runs it as a BSD Auth style).
install -o root -g root -m 0555 "$bundle/bin/filees-ssh-auth" "$libexec/filees-ssh-auth"
install -o root -g root -m 0644 "$bundle/lib/pam_filees_tunnel.so" "$libexec/pam_filees_tunnel.so"
install -o root -g root -m 0644 "$bundle/share/filees/linux/filees-tunnel.pam" /etc/pam.d/filees-tunnel
install -o root -g root -m 0555 "$bundle/bin/filees-onboard" "$libexec/filees-onboard"
# The dispatchers are the set-id boundaries; their ordinary child images
# inherit the effective state UID.
install -o "$state_user" -g root -m 4511 "$bundle/bin/filees-bootstrap-entry" "$libexec/filees-bootstrap-entry"
install -o "$state_user" -g root -m 4511 "$bundle/bin/filees-entry" "$libexec/filees-entry"
install -o root -g root -m 0555 "$bundle/bin/filees-worker" "$libexec/filees-worker"
install -o root -g "$client_access_group" -m 4550 "$bundle/bin/filees-service-wc-corrector" "$libexec/filees-service-wc-corrector"
install -o root -g root -m 0555 "$bundle/bin/filees-mail" "$libexec/filees-mail"
install -o root -g root -m 0555 "$bundle/bin/filees-public-authority" "$libexec/filees-public-authority"
install -o root -g root -m 0555 "$bundle/bin/filees-links" "$libexec/filees-links"
install -o root -g root -m 0555 "$bundle/bin/filees-serving-state" "$libexec/filees-serving-state"
install -o "$state_user" -g "$client_access_group" -m 4550 "$bundle/bin/filees-client-entry" "$libexec/filees-client-entry"
install -o "$state_user" -g "$client_access_group" -m 4550 "$bundle/bin/filees-mobile-v1" "$libexec/filees-mobile-v1"
install -o "$state_user" -g _filees-recovery -m 4550 "$bundle/bin/filees-recovery-entry" "$libexec/filees-recovery-entry"
# OpenSSH requires every AuthorizedKeysCommand component to be root-owned and
# not writable by group/other; this second image runs only "authorize".
install -o root -g root -m 0555 "$bundle/bin/filees-recovery-entry" "$libexec/filees-recovery-authorize"

install -o root -g root -m 644 "$bundle/share/filees/linux/bootstrap_authorized_keys" /etc/ssh/filees_bootstrap_authorized_keys
install -d -o root -g root -m 755 /etc/ssh/sshd_config.d
install -o root -g root -m 644 "$bundle/share/filees/linux/filees.conf" /etc/ssh/sshd_config.d/filees.conf

install -d -o "$state_user" -g root -m 700 /var/filees/activation /var/filees/activation/records /var/filees/activation/proofs /var/filees/sessions
install -d -o "$state_user" -g root -m 700 /var/filees/repositories /var/filees/repository-operations
install -d -o "$state_user" -g root -m 700 /var/filees-mobile /var/filees-mobile/ledger
install -d -o "$state_user" -g root -m 700 "$mobile_temp_root"
echo "Set mobile.temp_root=$mobile_temp_root in server.json; existing config is preserved."
install -d -o "$state_user" -g root -m 700 /var/filees/repository-operations/public-shares
# Created here, not on first use: under SELinux a directory made later would
# take the generic /var type and sshd could not read recovery keys from it.
install -d -o "$state_user" -g root -m 700 /var/filees/repository-operations/recovery-keys
install -d -o root -g root -m 755 "$public_downloads_dir"
install -d -o "$state_user" -g root -m 700 "$public_authority_staging_root"
install -d -o _filees-links -g root -m 700 "$public_links_cache_root"
install -d -o _filees-links -g "$public_access_group" -m 770 "$public_upload_intake_root"
for empty in authorized_keys repositories.authz service.authz; do
	if [ ! -e "/var/filees/activation/$empty" ]; then
		install -o "$state_user" -g root -m 600 /dev/null "/var/filees/activation/$empty"
	fi
done
if [ ! -d /var/filees/service-repo ]; then
	"$svnadmin" create /var/filees/service-repo
	"$svn" mkdir --non-interactive --no-auth-cache -m "filees: initialize proof" file:///var/filees/service-repo/proof
fi
if [ ! -d /var/filees/service-wc/.svn ]; then
	"$svn" checkout --non-interactive --no-auth-cache file:///var/filees/service-repo /var/filees/service-wc
fi
cat > /var/filees/service-repo/conf/svnserve.conf <<'EOF'
[general]
anon-access = none
auth-access = read
authz-db = /var/filees/activation/service.authz
EOF
chown -R "$state_user":root /var/filees/service-repo /var/filees/service-wc /var/filees/activation
chmod 700 /var/filees/service-repo /var/filees/service-wc /var/filees/activation

# SELinux (Fedora and relatives). sshd is confined, and three things run inside
# its domain before any login exists: the tunnel helper, the reader of
# authorized_keys and the recovery key lookup. The module gives the two state
# directories they use a type of their own and nothing more; see filees.cil.
# Paths moved in server.json need the same labels set by the administrator.
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then
	need semodule "SELinux policy tools"
	need restorecon "SELinux policy tools"
	semodule -i "$bundle/share/filees/linux/filees.cil"
	restorecon -R /var/filees/onboarding /var/filees/activation /var/filees/repository-operations/recovery-keys
	echo "SELinux is enabled: FileES policy module installed and state directories labelled."
fi

# An Include appended after somebody's Match block would become conditional;
# "Match all" closes any such block first.
if ! grep -Eq '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf[[:space:]]*$' /etc/ssh/sshd_config; then
	if [ ! -e /etc/ssh/sshd_config.filees-before-s2 ]; then
		cp -p /etc/ssh/sshd_config /etc/ssh/sshd_config.filees-before-s2
	fi
	printf '\nMatch all\nInclude /etc/ssh/sshd_config.d/*.conf\n' >>/etc/ssh/sshd_config
fi

chown "$state_user":root /var/filees
chmod 700 /var/filees
chown -R "$state_user":root /var/filees/onboarding
chmod 700 /var/filees/onboarding /var/filees/onboarding/tickets /var/filees/onboarding/operations /var/filees/onboarding/audit
chown "$state_user":"$public_access_group" /etc/filees
chmod 750 /etc/filees
chown "$state_user":root /etc/filees/server.json /etc/filees/otp.pepper /etc/filees/worker_ed25519
chmod 600 /etc/filees/server.json /etc/filees/otp.pepper /etc/filees/worker_ed25519
chown "$state_user":root /etc/filees/public-share-frost.key
chmod 600 /etc/filees/public-share-frost.key
chown _filees-links:root /etc/filees/public-links.json /etc/filees/public-share-visit.key
chmod 600 /etc/filees/public-links.json /etc/filees/public-share-visit.key
chown root:root /etc/filees/worker_ed25519.pub
chmod 644 /etc/filees/worker_ed25519.pub

sshd -t
# The unit is "ssh" on Debian and "sshd" on Fedora. A server that is not
# running is left alone: starting OpenSSH is the administrator's decision.
sshd_unit=
if command -v systemctl >/dev/null 2>&1; then
	for candidate in ssh sshd; do
		if systemctl cat "$candidate.service" >/dev/null 2>&1; then
			sshd_unit=$candidate
			break
		fi
	done
fi
if [ -n "$sshd_unit" ] && systemctl is-active --quiet "$sshd_unit"; then
	systemctl reload "$sshd_unit"
elif [ -n "$sshd_unit" ]; then
	echo "sshd configuration is valid, but $sshd_unit is not running; start it to accept FileES clients."
else
	echo "sshd configuration is valid; reload your OpenSSH server to apply it."
fi
echo "FileES accounts, binaries and SSH entries installed. Linux runs without a sandbox."
echo "Public shares were not configured; no listener and no web server were touched."
echo "Scheduled maintenance (passport, lock-request and upload reaping, capacity alerts) is not installed yet."
