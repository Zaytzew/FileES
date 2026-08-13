# FileES server toolchain bundle

This bundle contains the short-lived server toolchain plus two optional,
disabled-by-default Public Shares services:

- `filees-admin` and `filees-operation` are administrative commands;
- `filees-bootstrap-entry` is the bounded public-key forced command; it runs
  `filees-onboard take` followed by one `filees-mail send` attempt;
- `filees-onboard` consumes an invitation capability and creates the durable OTP mail outbox;
- `filees-ssh-auth` is the local BSD Authentication OTP style;
- `filees-entry` is the tunnel-account forced command;
- `filees-worker` is exec'd once per authenticated deploy and exits after its bounded action;
- `filees-client-entry` is the per-key forced SVN entry used for possession proof and active read-only access;
- `filees-mail send` submits one pending control-plane outbox entry; its
  `public-loop` mode is supervised by `filees-public-authority` and drains only
  Public Shares invitation/OTP mail without exposing the SMTP secret to the
  authority process; authority handles service-stop signals and terminates the
  child before exiting, so rc.d restarts do not leave orphan pollers;
- `filees-public-authority` exposes the credential-free Public Shares
  backchannel on a Unix socket or a loopback TCP endpoint;
- `filees-links` serves the public surface through FastCGI and owns only its
  temporary cache.

Run `install-server.sh` as the target system administrator, edit
`/etc/filees/server.json`, and keep both configuration and OTP pepper private.
Run state-mutating administrative commands with effective user
`_filees-state` (for example through a narrow `doas` rule); running
`filees-admin ticket create` as root would create a root-owned `0600` ticket
which the set-id onboarding command intentionally cannot read. The normal
command is `filees-admin ticket create user@example.com`: it uses
`/etc/filees/server.json`, a 24-hour TTL and immediately sends the first,
single-use activation invitation through the configured SMTP relay.
To authorize another desktop installation to join an existing realm, bind the
ticket server-side by immutable alias:

```text
doas -u _filees-state filees-admin ticket create user@example.com \
  --join-realm-alias existing-alias --ttl 24h
```

The server resolves the alias before issuing the invitation and stores the
approved realm ID in ticket policy. The client never supplies an existing
realm ID. A ticket created without `--join-realm-alias` authorizes a new realm;
revoke an incorrectly created unused ticket instead of trying to edit it.
The generic installer deliberately does not create an `rc.d`/systemd service
and does not modify `sshd_config`. On OpenBSD, the separate
`openbsd/install-ssh.sh` step creates the two protocol accounts, installs the
local login style and a validated `Match User` fragment, then reloads the
existing system sshd. It does not install a service, listener or `inetd` entry.

Public Shares are installed disabled. The OpenBSD step creates `_filees-links`
and `_filees-public`, installs disabled `filees_public_authority` and
`filees_links` rc.d scripts, and assigns the shared-topology paths as follows:

- canonical channel state: `_filees-state`, mode `0700`;
- authority socket directory: `_filees-state:_filees-public`, mode `0750`,
  socket mode `0660`;
- public cache: `_filees-links`, mode `0700`, under `/var/tmp` and outside
  backups;
- FastCGI directory: `_filees-links:www`, mode `0750`, socket mode `0660`.

`public_shares.max_size` limits one authoritative leaf before it can fill the
private staging filesystem; omission defaults to 1 GiB.
`max_channels_per_realm` defaults to 128 active/revoked channels, and
`password_required` can prohibit unauthenticated open channels. The separate
`public-links.json` `cache.max_size` is the hard total cache capacity and its
TTL cannot exceed 24 hours. The shipped values are 1 GiB per leaf, 10 GiB total
and 12 hours. Password verification is serialized, identical cache misses are
coalesced, and the authority runs at most two concurrent `svnlook` fetches.

The canonical public URL belongs to the FileES server's existing HTTPS origin:
`https://<server-domain>/<realm>/<slug>`. Merge the ordered locations from
`openbsd/public-links.httpd.conf` into that origin's `server` block. Static
paths are an explicit allowlist; the final `location "/*"` sends everything
else to `filees-links`. Do not route with `location not found`: filesystem
presence must never shadow a realm or share, and adding a realm must not create
a directory or reload httpd.

Enable and start the authority before links, validate the complete httpd
configuration, and only then reload httpd. Neither FileES binary terminates
TLS. A listener behind relayd can remain on loopback; a standalone installation
adds its normal TLS certificate and redirect blocks to the same system httpd
server. In a split topology keep the same backchannel protocol and point both
ends at loopback TCP provided by a server-established reverse SSH forward;
never expose the authority port on a public interface. An external short-link
service may redirect to the canonical URL, but is never required by FileES.

The shipped bootstrap private key is deliberately compiled into the client and
must be considered public. Its authorized-key entry can only reach the
enumeration-free onboarding forced command and has forwarding disabled.
The installer generates a server-local Ed25519 worker key. Distribute only its
public `.pub` file to the client policy which starts the loopback helper; the
private key remains mode 0600 under `/etc/filees`.

An existing S2 `server.json` must be extended with absolute
`worker_private_key_file` and `worker_public_key_file` paths before enabling
S3, and with an `invitation` profile containing the stable server ID, public
SSH endpoint and verified ED25519 `known_host` line. Re-running the generic
installer creates a missing keypair but never overwrites the existing
configuration.

Repository deletion policy lives under `repositories`. Omitted
`deletion_retention_days` defaults to 30. A positive value means: verified SVN
dump retained for that many days, with FSFS removed immediately after
verification. Explicit `0` is the panic policy: remove FSFS immediately and do
not create a dump. `deletion_archive_root`, when set, must be absolute; otherwise
the worker uses `results_root/deleted-repositories`. A custom root outside
`results_root` is an exact OpenBSD `unveil` target and must already exist as a
dedicated, mode-0700 directory before the worker starts.

Each retained deletion leaves two operator-visible artifacts named with the
repository and delete operation IDs: a `.svndump` and a JSON manifest. The
manifest records `created_at`, `delete_after`, the dump filename and its
SHA-256; the durable `results/<operation_id>.delete_repository.json` records
the same `retain_until`. Keep both files together for the full retention
period. The active FSFS directory and its generated authz projection are gone
as soon as the deletion succeeds.

`repositories.root` is likewise an absolute, configurable FSFS storage root;
`/var/filees/repositories` is a default, not a required mount point. Moving an
existing root is a maintenance operation: stop all writers, verify every source
repository with `svnadmin verify`, copy every direct child repository with
ownership and modes preserved, verify the copy, then change only
`repositories.root` in `/etc/filees/server.json`. Keep the original root
read-only until acceptance and backup checks complete, so rollback remains a
configuration change. Do not use a live move or allow writes to both roots.
The full OpenBSD procedure is in the administrative recovery section of
`manual-filees.html`.

## OpenBSD upgrade boundary

Produkcyjny bundle instaluje teraz `filees-install` oraz zachowawcze
`/etc/filees/install.conf`. Po opublikowaniu podpisanego release’u bazowego
uruchom `filees-install --adopt <release-id>`: komenda niczego nie podmienia i
zaakceptuje serwer tylko wtedy, gdy hash, właściciel, grupa i pełny tryb każdego
zarządzanego pliku dokładnie odpowiadają manifestowi. Następne upgrade’y należy
wykonywać przez `filees-install --dry-run`, a potem `--apply`; trwały journal
zapewnia pełne odtworzenie pre-image po przerwanym apply.

On an already integrated OpenBSD host, `install-server.sh` alone is not a safe
complete upgrade. The generic installer writes ordinary `0755` modes, while
`openbsd/install-ssh.sh` assigns the required set-id ownership and modes to the
protocol entry binaries. Running only the generic script can therefore make
onboarding or client entry lose access to `_filees-state` files.

For a full bundle upgrade, run both installation stages, validate with
`sshd -t`, and retain a recoverable copy of the previous binaries. For a
targeted update of an ordinary short-lived binary such as `filees-worker`, an
operator may instead install a temporary file as `root:wheel 0555` and rename
it atomically over `/usr/local/libexec/filees/filees-worker`. FileES has no
resident worker service to restart; the next authenticated operation execs the
new image. Never overwrite `filees-bootstrap-entry`, `filees-entry`,
`filees-client-entry`, mobile or recovery entries without restoring their exact
OpenBSD ownership and set-id modes.
