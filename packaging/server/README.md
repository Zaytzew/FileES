# FileES server toolchain bundle

## Identify a deployed build

Run `filees-admin version` (also `--version` or `-version`). It reports the
binary version with SVN revision, target platform, `update_channel` from
`/etc/filees/install.conf`, and `recorded_release` from installer state.
For a custom installation use `filees-admin version --install-config /path/install.conf`.
This is read-only, requires no `server.json` or service-account transition,
and never contacts the release repository. Unreadable metadata is `unknown`;
run as an account allowed to read installer configuration/state (normally root).

The channel is the configured subscription, not a compiled property or proof
that the installed release came from that channel. The recorded release does
not prove that all resident services have restarted. The same signed artifact
can be promoted from alpha to beta unchanged.

Builds stamp `VERSION+r<svnversion>`; mixed revision ranges use a dash, and
SVN's `M` (modified), `S` (switched), `P` (partial) markers remain visible.
Exports or builds without SVN discovery say `+unversioned` rather than claiming
a release revision. Official release preparation requires an exact clean
revision matching the manifest. The bundle's `VERSION` contains the same stamp.

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
  `public-loop` mode is supervised by `filees-public-authority` and drains
  Public Shares invitation/OTP mail and upload-shelf invitations without exposing the SMTP secret to the
  authority process; authority handles service-stop signals and terminates the
  child before exiting, so rc.d restarts do not leave orphan pollers;
- `filees-public-authority` exposes the credential-free Public Shares
  backchannel on a protected Unix socket (raw TCP is rejected);
- `filees-links` serves the public surface through FastCGI and owns only its
  temporary cache and, when configured, the upload-shelf intake quarantine;
- `filees-worker upload-reap` is the hostadmin cron that moves a ready intake
  job through AV into `upload_repo` or the reject tree. The installer does
  not install that crontab. Run it as `_filees-state` and redirect stdout
  (`>/dev/null`) so an idle minute does not mail `accepted=0`.

## Activation nick blocklist

`nick-blocklist.txt` is the default list of vulgar and offensive fragments
that an activation nick must not contain
(`implementation notes (not distributed)` §4.1). It belongs at
`/etc/filees/nick-blocklist.txt`, next to `server.json`, owned by
`_filees-state`. An installer places it only when the file is absent and
never overwrites an administrator's edits; no installer does that yet.

Format: plain UTF-8, one lowercase fragment per line using only the nick
alphabet, blank lines and `#` comments ignored, matched as a substring.
A missing file or an invalid line must stop nick issuance with a specific
error rather than run with an empty list.

The file is generated, not edited by hand. It is built from the LDNOOBW
word lists (PL, EN, DE, FR, ES; CC-BY-4.0, credited in its header):

```sh
go run ./tools/nick-blocklist -out packaging/server/nick-blocklist.txt \
  -sample 100000 -attribution "Word lists: LDNOOBW ... CC-BY-4.0" pl.txt en.txt de.txt fr.txt es.txt
```

The generator keeps only fragments of 3–9 letters spellable with the nick
alphabet and drops any entry already covered by a shorter one. The shipped
list has 175 fragments and blocks about 1.5% of random nicks.

## Passport expiry maintenance (M45)

`filees-worker passport-reap -config /etc/filees/server.json` performs one
server-clock cleanup pass over registered acquisitions, under the existing
worker/service-WC locks. Run as `_filees-state`, never root. It needs no
connected client, activation secret, grant lookup or service-WC update.
It releases only the exact recorded token; ordinary and unregistered legacy
locks are not globally scanned or force-released. Existing scoped admin
`repo reap-passports --path ...` remains available for a legacy passport.

The bundle ships `share/filees/openbsd/passport-reap.crontab.example`; initial
OpenBSD installation copies it to `/etc/examples/filees-passport-reap.crontab`.
It does **not** enable cron or overwrite a crontab. Binary-only upgrades may
require copying the example separately. During an explicitly authorized
rollout, merge its lines into the `_filees-state` crontab, preserving existing
jobs. The example runs once a minute; errors remain on stderr for cron mail.
Verify cron and delivery to the chosen MAILTO address.
The example delays its five-minute health probe by 50 seconds to avoid the
normal start of the 45-second pass; external probes should likewise run
between passes because observing running deliberately fails the check.

`passport-reap --check` (after any `-config path`) is a read-only health check:
nonzero exit for failed/running/missing/corrupt status, clock rollback or a
completion older than five minutes. State is stored atomically in
`repositories.results_root/passport-maintenance/status.json`. Also run this
check from external monitoring: a check in cron alone cannot alert when the
entire cron service or host stops. No GUI alert is installed by this change.

Each pass has a 45-second context deadline; native command pipe waits are
bounded after process termination. Busy locks return without queuing; a busy
record does not block other records. A failed/dead pass is not acknowledged:
the next invocation resumes from the durable registry. Never delete closed
records or `.lock` files to clear an alarm. An old binary is not resident:
the next cron invocation executes the updated worker.

The owner accepted operational expiry at the next successful pass, not a
strict SVN expiration deadline. During failures an administrator may resolve
the lock explicitly, or use the existing fork/replace workflow. Automatic
force, deleting pending records or discarding local changes are not fallbacks.
Enable the coherent server/broker/hook v2 before the new client, drain old SVN
sessions and inspect foreign hooks; maintenance does not perform that rollout.

Run `install-server.sh` as the target system administrator, edit
`/etc/filees/server.json`, and keep both configuration and OTP pepper private.
Schema `filees.server-toolchain/v2` requires a top-level `display_name`: the
human-facing server name projected to every client. `invitation.server_id` is
immutable technical identity and must never be renamed to change the UI.
After changing `display_name`, run
`doas -u _filees-state filees-operation recover`; it advances all active
`view.json` files to `filees.client-view/v2`, increments their generations and
publishes them in one service-repository commit. This is an intentional hard
contract cut: deploy a matching client before publishing v2 projections.
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

### Abandoned repository-creation cleanup

An activation or first-import failure can exceptionally leave a durable
repository record that was published to client views but never became active.
Inspect these records before changing anything:

```text
doas -u _filees-state filees-admin repo check-state
doas -u _filees-state filees-admin repo check-state --realm-id <uuid>
```

`check-state` correlates the backend record, canonical authority state,
FSFS/staging presence and `svnlook youngest`. A normal active repository is
omitted. A published candidate is marked `prunable:true` only when its
canonical state is `initializing`, no staging tree exists, and FSFS is absent
or exactly r0.

Always inspect the dry run before applying it:

```text
doas -u _filees-state filees-admin repo prune \
  --realm-id <uuid> --older-than 1h
doas -u _filees-state filees-admin repo prune \
  --realm-id <uuid> --older-than 1h --apply
```

`--older-than` defaults to one hour. `--apply` durably records
`prune_pending`, installs a commit blocker, verifies HEAD again, then removes
the abandoned repository from canonical projections/data-authz, deletes the
empty FSFS and finally removes its backend record. An interruption is
retryable. Active repositories, initializing repositories at r1 or later,
ordinary deletion tombstones, staging trees and uncertain states are never
removed automatically; they appear under `needs_attention` instead.

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
- authority staging: `_filees-state`, mode `0700`, at configured
  `public_shares.authority_staging_root` (default `/var/filees-downloads/authority`);
- public cache: `_filees-links`, mode `0700`, at configured `cache.root`
  (example `/var/filees-downloads/cache`), outside backups;
- upload intake: `_filees-links:_filees-public`, mode `0770`, under
  `/var/filees-downloads/intake` (override `PUBLIC_UPLOAD_INTAKE_ROOT`);
  job subdirectories are also `0770` so
  `_filees-state` (in `_filees-public`) can reap them;
- FastCGI directory: `_filees-links:www`, mode `0750`, socket mode `0660`.

The private AV rejection waiting room (`server.json` `upload.trash_root`,
owned by `_filees-state`, mode `0700`) has a separate optional byte limit:
`upload.max_trash_size`. The example sets **10 GiB** (`10737418240`);
zero or omission disables it, so existing configurations need an explicit
setting. All writers using this root must use the same limit and upgraded
code. A kernel lock serializes copying, reading and TTL maintenance; a busy
root refuses the concurrent operation, which may be retried.
Hidden payloads and incomplete copies count. Metadata and filesystem
allocation overhead do not: this is not a filesystem quota.
On exhaustion, upload-reap reports failure and retains the intake job;
the 48-hour retention is never shortened to make space. Configure the
separate intake budget too, otherwise the retained queue can keep growing.
Unknown files are preserved and counted, not removed based on age.

Upload intake is a persistent delivery queue, **not** a cache: never put it
under `/tmp` or `/var/tmp`, including through symlinks. On OpenBSD, daily
cleanup can remove an empty directory and invalidate the running service's
inode-based unveil access even if the directory is later recreated. Changing
paths requires coordinated configuration and a service restart, not a cron
job that recreates the directory. The example AV command uses `clamdscan
--stream`: the state worker reads the payload and sends bytes to clamd, whose
user does not need traversal permission on the private intake tree.

The bootstrap scripts still preserve existing JSON files and do not rewrite
examples for `PUBLIC_*` overrides. Set `server.json` `upload.intake_root` and
`public-links.json` `intake_root` to the same selected directory. The default
change does **not** migrate existing queues or enable the upload reaper.
Resource-aware selection, coordinated JSON editing and post-install readiness
validation remain pending; successful file installation is not proof that
upload is ready. Preserve pending payloads during any operator-approved
migration. Native OpenBSD acceptance is required before release.

For existing managed Public Shares storage migrations, `filees-install
--check`, `--dry-run` and `--apply` now print a `STORAGE` capacity report before
payload staging: resolved target paths, filesystem device ID, available bytes,
combined additional required bytes and reserve. Different paths on one
filesystem share one budget. An insufficient volume is refused with a hint to
select a larger `install.public_downloads_dir`. Capacity is checked again
before directory preparation; neither check reserves space. This is scoped to
planned storage migrations, not all existing repositories, upload queues or
the shell bootstrap. A run with no storage migration does not certify capacity.

`public_shares.max_size` limits one authoritative leaf before it can fill the
private staging filesystem; omission defaults to 1 GiB.
`max_channels_per_realm` defaults to 128 active/revoked channels, and
`password_required` can prohibit unauthenticated open channels. The separate
`public-links.json` `cache.max_size` is the hard total cache capacity and its
TTL cannot exceed 24 hours. The shipped values are 1 GiB per leaf, 10 GiB total
and 12 hours. Password verification is serialized, identical cache misses are
coalesced, and the authority runs at most two concurrent `svnlook` fetches.

Both download roots must be on persistent, sufficiently large storage outside
system temporary-directory cleanup. ZIP output is streamed, but all selected
leaves are first materialized in cache. Physical free space and the configured
leaf/cache/ZIP limits are separate constraints. Storage admission uses 64-bit
sizes and a 16 MiB margin, not a reservation. Storage failures after successful
authorization return generic HTTP 503 with Retry-After; denials remain 404.

For bootstrap, both shell scripts accept `PUBLIC_DOWNLOADS_DIR`, with optional
independent `PUBLIC_AUTHORITY_STAGING_ROOT` and `PUBLIC_LINKS_CACHE_ROOT`
overrides. Use identical settings for both stages and edit the installed JSON
paths to match before starting services: scripts preserve existing configs and
do not rewrite example JSON. They are bootstrap scripts, not an upgrade path.
For upgrades use `install.public_downloads_dir` and the transactional migration.
See [public-storage-migration.md](public-storage-migration.md), especially the
two-invocation bridge from an older running installer. Do not restart before
the migration has completed. The operator excludes staging and cache from
backups; changing location does not itself enforce that exclusion.

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
server. In a split topology keep the same backchannel protocol and use protected
Unix sockets at both ends of a server-established reverse SSH forward;
raw backchannel TCP is no longer accepted, including loopback. Before upgrading
a TCP deployment, follow [the Unix backchannel migration](BACKCHANNEL_UNIX.md).
An external short-link
service may redirect to the canonical URL, but is never required by FileES.

The shipped bootstrap private key is deliberately compiled into the client and
must be considered public. Its authorized-key entry can only reach the
enumeration-free onboarding forced command and has forwarding disabled.
The installer generates a server-local Ed25519 worker key. Distribute only its
public `.pub` file to the client policy which starts the loopback helper; the
private key remains mode 0600 under `/etc/filees`.

An existing S2 `server.json` must be changed to schema v2, given a top-level
`display_name`, and extended with absolute
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
The full OpenBSD procedure is in the HTML manual
(`manual/assets/en/install.html` and `manual/assets/en/administration.html`)
and in the OpenBSD manual pages shipped with this bundle
(`man filees`, `man filees.conf`, `man filees-admin`).
The HTML set is the Subversion subtree `^/manual`. On
`manual.filees.space` the document root is that working copy:

```text
svn checkout svn://cloud.atmprojekt.pl/SYNCSHARE/manual /var/www/htdocs
svn update /var/www/htdocs
```

Chapters live under the existing `/assets/*` allowlist. Do not check the
tree out as `/var/www/htdocs/manual` and do not reload `httpd`.

The full manual pages under `manual/assets/man/` are generated from
`docs/man` by `mandoc -T html`. After editing a page in `docs/man`, run
`go run ./tools/manual-man-pages` and commit both. A new HTML file needs
`svn propset svn:keywords Rev`, like every other page. If the HTML is not
regenerated, `packaging/man_html_test.go` fails: each page records the
SHA-256 of its source.

## OpenBSD upgrade boundary

Strony `mandoc` są częścią podpisanego wydania serwera (od 2026-09-29).
`filees-install --apply` aktualizuje je razem z binariami, które opisują: pliki
`/usr/local/man/man{5,7,8}/filees*`, root:wheel, tryb 0444, tak jak zostawia je
`install-server.sh`. Cele są zapisane jako ścieżki bezwzględne, bo instalator
sprzed nowej zmiennej katalogu zostawiłby ją nierozwiniętą. `man` znajduje nową
stronę od razu. Indeks `apropos` odświeża cotygodniowy `makewhatis`; od ręki:
`makewhatis /usr/local/man`. Instalacja z inną wartością `PREFIX` dostaje
strony w `/usr/local/man`.

Produkcyjny bundle instaluje teraz `filees-install` oraz zachowawcze
`/etc/filees/install.conf`. Po opublikowaniu podpisanego release’u bazowego
uruchom `filees-install --adopt <release-id>`: komenda niczego nie podmienia i
zaakceptuje serwer tylko wtedy, gdy hash, właściciel, grupa i pełny tryb każdego
zarządzanego pliku dokładnie odpowiadają manifestowi. Następne upgrade’y należy
wykonywać przez `filees-install --dry-run`, a potem `--apply`; trwały journal
zapewnia pełne odtworzenie pre-image po przerwanym apply.

Zmiana generacji `/etc/filees/server.json` jest własnością instalera, nie
procesów runtime. Manifest deklaruje przejście schematu w `configs[].default_changed`;
`check` i `dry-run` pokazują `CONFIG MIGRATE`, a `apply` zapisuje przekształcony
config jako ostatni element tej samej transakcji co binaria. Backup, journal,
rollback, tryb i ownership obejmują również config. Runtime pozostaje ścisły i
nie implementuje zgodności ze starą generacją.

Pierwsze wdrożenie tej zdolności ma granicę bootstrapu: instalator już
uruchomiony z poprzedniego release'u nie zaczyna wykonywać kodu binarium, którym
właśnie zastąpił sam siebie. Na hostach, które dostały już serwerowe binaria v2
przez starszy updater, należy najpierw dostarczyć nowy podpisany
`filees-install`, a dopiero jego procesem wykonać właściwy `apply`. Kolejne
zmiany generacji nie wymagają tej jednorazowej operacji.

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

### Lock release request maintenance

`filees-admin -config /etc/filees/server.json repo reap-lock-requests`
reconciles pending requests and removes terminal records after seven days,
only when the exact original lock token is no longer live. Projection removal
is committed before deleting the record. An unavailable authority or failed
publication retains the record. Repository-control also runs this pass.
`lock-requests-reap.crontab.example` supports cleanup without client activity;
installation copies the example but does not activate cron. Run as
`_filees-state` with the existing service WC/publication permissions.

### Server alert channel (desktop consumer)

The operator can publish a safe incident message to the active desktops of one
explicitly selected realm. This grants no administrative actions to recipients.
The publisher uses the service repository through `svnmucc`; no additional WC
is created. `svnlook` and `svnmucc` must be available in the configured toolchain.

```sh
doas -u _filees-state filees-admin alert publish \
  --realm <realm-uuid> --key storage.var --severity error \
  --text 'Brak miejsca na serwerze; wysyłka może być niedostępna.'

doas -u _filees-state filees-admin alert publish \
  --realm <realm-uuid> --key storage.var --severity error --resolve \
  --text 'Dostępne miejsce zostało przywrócone.'
```

`--key` identifies the problem. Repeating unchanged content is a no-op; a
recurrence after resolution starts a new incident. `--resolve` updates clients
silently. The text must be safe for every active desktop of that realm: do not
include credentials, raw tool errors or another customer's information.

Mailboxes live at `alerts/<realm-uuid>/snapshot.json` in the service repository.
Publication regenerates service authz from canonical activations: only active
desktops of the addressed realm can read it; mobile and staged activations have
no channel access. SVN revision log messages contain no incident text. Desktop
read receipts stay on that desktop; they do not change the server snapshot.

Deploy matching daemon/GUI code for the consumer. Polling uses native SVN,
bounded snapshots (256 KiB, 128 records), a separate read lane and 1–5 minute
backoff. Existing services without a mailbox remain compatible. No automatic
capacity monitor, new administrator role or mobile consumer is installed by
this feature. A full filesystem may prevent publication itself; this channel
cannot replace direct operation errors or independent infrastructure monitoring.
Resolved records are pruned from snapshots when needed; this does not reclaim
SVN history. Automated channel history rotation is not part of this first stage.


## Capacity alerts and independent email fallback

`filees-admin alert capacity` performs one filesystem-metadata pass (Linux or
OpenBSD); no file-tree scan. It uses the existing server SMTP configuration.
Copy `capacity-alerts.example.json` to `/etc/filees/capacity-alerts.json`, set
`realm_id`, `admin_email`, and a private `state_dir` writable by `_filees-state`.
The recipient realm is an explicit operator choice; this does not introduce
administrator roles. Keep the config readable by the state user, mode 0600.
Prefer a state directory on a different filesystem from the service repository.
Prepare its parent with the correct ownership before enabling the monitor.

Defaults: warning below 15% available blocks/inodes OR 1 GiB available bytes;
critical below 5% OR 256 MiB. Recovery requires an additional 2 percentage points
and 128 MiB above the triggering level. For small system volumes configure lower
absolute thresholds using `policy.warning_bytes`, `critical_bytes`, and
`hysteresis_bytes`. Other policy keys: `warning_percent`, `critical_percent`,
`hysteresis_percent`. A volume is sampled once even when several configured paths
use it. Add `paths: [{"label":"install-stage","path":"/actual/stage"}]` for
storage outside server.json (installer staging/backups, public-links cache, etc.).
Missing future directories use their nearest existing parent; this is not a
mount-presence monitor. `--dry-run` prints measurements without sending or saving.

New incidents/escalations go to both the GUI channel and SMTP. Resolution is
silent. A failed channel write does not suppress the independently queued email;
an SMTP failure retains its intent for the next invocation. SMTP acceptance is
not proof of delivery to the mailbox. Crash after acceptance can retry the same
Message-ID. Failure to persist the local intent is an error; this is not an
out-of-band alert system when every writable filesystem is full. Errors reach
stderr and cron mail; verify the system's cron-mail delivery too.

Current `filees-install --check` reports a missing/stale capacity cron entry;
`--apply` installs/repairs only its marked line in `_filees-state`'s crontab,
preserving other jobs. Bootstrap scripts do the same after account creation.
Install and enable cron/crond on Linux; there is no additional FileES service.
The signed example file identifies releases supporting this job. To repair a
legacy installation once the new installer is present:

```sh
/usr/local/sbin/filees-install --ensure-capacity-cron /usr/local/sbin /etc/filees/server.json
```

The job is installed even before the real configuration exists. `--scheduled`
is then a quiet no-op: monitoring starts only after the operator configures the
real recipient, email and state directory. Existing settings are never replaced
with the example. Installing with an old installer cannot add this new behavior;
use the new installer or the repair command for the first rollout. Binary rollback
to a version without `alert capacity` requires removing only the marked cron line;
keep the settings and state for the next upgrade. No production cron was enabled
by the development tests themselves.


## Mobile upload temporary filesystem

In `/etc/filees/server.json`, configure a dedicated directory on a volume
with enough working space (the example uses `/var/filees-mobile/tmp`):

```json
"mobile": { "temp_root": "/path/on/capacity-volume/filees-mobile-tmp" }
```

On OpenBSD prepare it as root, replacing the example path with your selection:

```sh
install -d -o _filees-state -g wheel -m 700 /path/on/capacity-volume/filees-mobile-tmp
```

The worker reads this setting on each new SSH invocation. It validates an
existing private directory and uses it as `TMPDIR` for the received spool,
ZIP extraction, transient SVN working copy and SVN child processes. Its
sandbox grants the same temporary root; an invalid configured path fails
closed, without falling back to `/tmp`. The root must not be a symlink.
Do not change the path while an upload is active; finish or stop the old
invocation first. The setting does not move existing temporary files or
change repository/ledger locations. It is separate from public upload intake.

Bootstrap scripts accept `MOBILE_TEMP_ROOT` and prepare the directory (the
OpenBSD SSH stage sets ownership to `_filees-state`). They preserve an existing
`server.json`; match its `mobile.temp_root` to the prepared path. Other systems
must give the actual mobile worker account ownership. Existing configurations
without the field retain the previous process `TMPDIR` behavior. Old binaries
reject the new field: upgrade the server before adding it.

The capacity monitor includes the configured root. Allow space for several
simultaneous representations of each upload and concurrent workers: available
space equal to the source file size is insufficient. This setting supplies
neither a reservation nor an exact peak-space guarantee. Normal completion
and errors clean their temporary files; a killed process can leave remnants.

`mobile.max_download_size` optionally limits one mobile download in bytes.
Zero or omission disables the size limit; the example sets 1 GiB
(`1073741824`). The worker checks the file size at the same revision it
reads, then checks the actual spool filesystem for that size plus a 16 MiB
reserve. It never writes beyond the measured size. Refusal is framed as
`download.limit` or `storage.unavailable`, without a success header or
partial payload. Ordinary failures remove the attempt's temporary file.
Linux/OpenBSD also reserve the measured size under a shared kernel lock.
`mobile.max_read_spool_size` limits the sum of these reservations (zero or
omission disables the quota; example: 4 GiB). All workers sharing the same
root must use the same policy and upgraded code. Free-space admission
conservatively counts active reservations in addition to the new one;
materialized bytes may therefore be charged twice in this capacity check.
It cannot reserve physical space against unrelated applications.

Each attempt holds its own kernel lock through both file creation and the
entire send. The next admission or normal close reclaims abandoned attempts
only after acquiring their lock. No age/PID guesses, no recursive deletion;
unknown files and symlinks block admission and require inspection.
New buffers live in the private `filees-mobile-reads-v1` subdirectory of the
effective temporary root; old files outside it are never reclaimed this way.
The pool and its `.maintenance.lock` persist when empty. Stop all workers
before changing/moving its root; never unlink a live lock file.
This quota covers download payloads, not metadata, uploads or dump imports.
Cleanup is opportunistic, not a deadline after a crash or reboot.
Upgrade the server before adding the field; no client or repository migration
is needed. The new error code/message travels through the existing error frame.

### Recovering an old mobile operation after an upgrade

`operation.uncertain` means the server cannot prove the outcome of an older
attempt; it does not mean the upload succeeded. Keep the phone queue and ledger.
Use the ledger owner account (normally `_filees-state`), so atomic writes retain
worker access. The command is local administration, never a phone operation:

```sh
doas -u _filees-state /usr/local/sbin/filees-admin mobile recover --request-id UUID
```

This takes the per-operation lock and searches the repository's full history
for `filees:request-id`. If found, it restores the original receipt without
replaying data, even if newer edits exist. Without a matching commit or durable
no-change receipt, it refuses to authorize another write.

To permit retry of an unresolved legacy attempt, first prevent new mobile SSH
sessions and stop/wait for **all** old mobile workers and their SVN children.
A paused phone alone does not prove there are no orphan server processes.
Verify that maintenance state, then run:

```sh
doas -u _filees-state /usr/local/sbin/filees-admin mobile recover \
  --request-id UUID --allow-retry --confirm-workers-stopped \
  --reason 'Maintenance: mobile sessions disabled and all old worker/SVN processes stopped'
```

The flag is an operator attestation, not automatic process detection. Old
processes did not inherit the new fence, so a free operation lock alone is
insufficient. The command rechecks history under the lock, writes a private,
fsynced `recovery-UUID-*.json` audit (before image, planned decision, operator UID,
reason), then atomically updates the existing record. If interrupted, an audit
may describe a planned action not yet applied. Retry is safe; inspect the ledger
for the actual outcome. It never deletes the ledger or creates a new request ID.
Run without `--allow-retry` whenever the quiescence condition is not assured.

After successful reconciliation, restore mobile access and resume the same
queue. `REJECTED` plus `recovery_fenced=true` authorizes retry, not success.
`COMMITTED` identifies an existing saved revision. A busy operation, inaccessible
repository, invalid ID or failed audit leaves the record unchanged.

Mobile capacity errors now use `storage.full`; Go ENOSPC/EDQUOT and SVN E000028
are classified without disclosing paths in the response. Detailed diagnostics
remain in the local errors.log where writable. Android retains the upload and
its request ID for retry after storage is available. Success stays silent.
