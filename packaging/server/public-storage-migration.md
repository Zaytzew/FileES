# Public Shares — persistent download storage (M47)

This release supplies configurable storage and migration, not an automatic
service restart. Signing, channel promotion and host rollout are separate.

Cloud checkpoint recorded 2026-09-08: signed r922 / alpha (FILEES-BIN r59),
both installer passes, `/home/_filees`, authority/links restart and checks,
two real files, Range and bounded ZIP all passed. Do not repeat this bridge
on cloud as an unfinished task. This source guide update does not modify
the immutable r922 release. Evidence:
[storage recovery report](../../reports/PUBLIC_SHARES_STORAGE_RECOVERY_2026-09-07.md).

## Configuration contract

- `/etc/filees/install.conf`, section `[install]`: `public_downloads_dir` is
  the base for migrating legacy default paths. Default: `/var/filees-downloads`.
- `/etc/filees/server.json`: `public_shares.authority_staging_root` is the
  actual authority staging path; it is independently configurable.
- `/etc/filees/public-links.json`: `cache.root` is the actual frontend cache
  path; it is independently configurable, including another filesystem.

For cloud the owner selected `public_downloads_dir=/home/_filees`, producing
`/home/_filees/authority` and `/home/_filees/cache`. This is a host choice, not
a product constant. Free space measured after owner cleanup on 2026-09-07:
13.3 GiB on `/home`; measure again before applying. Default admission budget is
12 GiB (10 GiB cache + two 1 GiB leaves) plus 16 MiB headroom. Changing
`public_shares.max_size` or `cache.max_size` changes the migration budget.
Admission is advisory, not a disk reservation. Plan/dry-run does not provision
directories or reserve capacity; apply checks space before publishing configs.

The base parent must already exist, be root-owned and not writable by
group/others. The dedicated base is root:wheel 0755; authority is
_filees-state:wheel 0700; cache is _filees-links:wheel 0700. Existing metadata
must match: migration refuses to silently chmod/chown existing paths. Service
accounts must already exist (the normal two-stage bootstrap creates them).
Roots must not resolve into `/tmp` or `/var/tmp`. Checks do not certify mount
presence or recognize every temporary filesystem mounted elsewhere. Exclude
both directories from backups and external cleanup; do not make them web roots.

## Upgrade from an older installer

An installer already running in memory does not gain migration code when it
replaces its own binary. Use the following bridge, after the release has been
signed and promoted. `rNNN` below means this release's actual source revision.

1. With the old install.conf unchanged, run the existing verified update:
   `doas filees-install --apply rNNN`. Do not restart Public Shares yet.
2. Add `public_downloads_dir=/home/_filees` to the existing `[install]` section
   on cloud (select another persistent base on other hosts). Do not add this
   new key before step 1: the older strict config parser does not know it.
3. Start a **new invocation** of the installed `filees-install`. Check
   `--check-config`, then `--dry-run rNNN`. The plan must name both expected
   config migrations if both services still use the old default roots.
4. Run `doas filees-install --apply rNNN` again. The new installer preflights
   capacity, prepares private directories and installs both JSON changes with
   the existing pre-image journal. Applying the same release is permitted;
   signature and hash verification remain mandatory. Do not bypass checks.
5. Review the installed paths and permissions; then restart in order:
   `doas rcctl restart filees_public_authority filees_links`.
6. Obtain a fresh public visit and verify listing, one real file, Range/resume
   and a bounded ZIP. Listing alone is not evidence of download recovery.

Migration is enabled only by the signed release's
`filees.public-download-storage/v1` contract marker. It changes missing/empty
authority staging or legacy `/tmp`/`/var/tmp` default roots; arbitrary custom
paths remain untouched. The authority's normal absent `max_size` defaults to
1 GiB. An already-custom root is not moved merely because the base setting
changed: changing such an installation is a deliberate operator config edit
and requires separately prepared directories plus a service restart.

No channels, grants, keys, FSFS content or HTTP routing are changed. Old caches
are neither copied nor deleted. Backup/rollback restores the prior config
bytes and metadata; newly created empty dedicated directories may remain.
Rollback can restore a broken temporary path, so it is not itself service
recovery. Never remove or recreate a live unveiled root and expect the running
process to find it again. Do not run either bootstrap shell script as an upgrade.

## Capacity and errors

ZIP is streamed to the client, but its selected leaves must first fit in the
cache. Two simultaneous authority fetches can also consume staging space.
The three logical limits (leaf, cache, ZIP) are not filesystem capacity; all
use 64-bit byte sizes. Runtime checks leave headroom and classify local writer
failures even if space disappears after admission. Valid authorized requests
receive generic 503/Retry-After for storage failure, without private paths;
authority denial is still the enumeration-resistant 404. No forced cache
eviction, automatic backup edits or automatic rcctl restart is introduced.

## Retention boundary — open M48

In r922, `filees-links` sweeps expired metadata-backed cache entries before
Put and removes the requested expired/corrupt entry on Open. No periodic
Flush is wired into the service. TTL (12 h on cloud) starts at insertion;
hits do not renew it, but idle files can remain beyond TTL with no deletion
deadline. The sweep does not collect orphan data/tmp files without metadata,
and cache Remove errors are ignored. Authority staging is removed on Close
or ordinary error cleanup; a process crash can leave files behind.

M47 fixes root lifecycle and capacity checks, not autonomous GC or erasure
completion. Do not use TTL as evidence that physical bytes are gone. Keep
roots out of system cleanup; do not delete active transfers by file age.
Safe orphan detection, periodic cleanup and observable removal failures are
an open, separately scoped task (M48), not installed cron in this release.
