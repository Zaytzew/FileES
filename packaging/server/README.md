# FileES server toolchain bundle

This bundle contains short-lived tools, not services:

- `filees-admin` and `filees-operation` are administrative commands;
- `filees-bootstrap-entry` is the bounded public-key forced command; it runs
  `filees-onboard take` followed by one `filees-mail send` attempt;
- `filees-onboard` consumes an invitation capability and creates the durable OTP mail outbox;
- `filees-ssh-auth` is the local BSD Authentication OTP style;
- `filees-entry` is the tunnel-account forced command;
- `filees-worker` is exec'd once per authenticated deploy and exits after its bounded action;
- `filees-client-entry` is the per-key forced SVN entry used for possession proof and active read-only access;
- `filees-mail` submits one pending outbox entry to a configured smarthost.

Run `install-server.sh` as the target system administrator, edit
`/etc/filees/server.json`, and keep both configuration and OTP pepper private.
Run state-mutating administrative commands with effective user
`_filees-state` (for example through a narrow `doas` rule); running
`filees-admin ticket create` as root would create a root-owned `0600` ticket
which the set-id onboarding command intentionally cannot read. The normal
command is `filees-admin ticket create user@example.com`: it uses
`/etc/filees/server.json`, a 24-hour TTL and immediately sends the first,
single-use activation invitation through the configured SMTP relay.
The generic installer deliberately does not create an `rc.d`/systemd service
and does not modify `sshd_config`. On OpenBSD, the separate
`openbsd/install-ssh.sh` step creates the two protocol accounts, installs the
local login style and a validated `Match User` fragment, then reloads the
existing system sshd. It does not install a service, listener or `inetd` entry.

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
