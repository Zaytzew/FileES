# FileES native SVN client — explicit platform integration

Windows adapter checkpoint 2026-09-08, base r995 plus working delta:
`pkg/client/native_ra.go` routes managed-local info, checkout/update,
commit, lock/unlock, bounded log and CatTo when the existing Windows opt-in
is enabled. `svnfetch` uses CatTo for Windows distribution downloads.
Native errors never trigger a retry using CLI. Commit callback `null`
does not publish a foreign HEAD or consume a shout; lock outcomes are checked
per path, even at exit 0. The subsequent 2026-09-09 target-list checkpoint
removes the 512-target commit barrier without splitting transactions.

`commit --targets-stdin` reads UTF-8 paths terminated by NUL (including the
last path). It is mutually exclusive with argv targets. The entire stream
is validated before opening the WC or contacting the repository: at most
65536 paths and 16 MiB, no duplicates, invalid UTF-8, control characters,
empty records, traversal or metadata paths. EOF mid-record refuses. Stdin
is binary on Windows and does not depend on Windows command-line length.
The Go adapter always uses this mode after checking feature
`commit_targets_stdin_v1`; an older helper refuses before the commit.
No temporary response files or split transactions. The separate local/argv
path batches remain 512; this does not remove every possible argv limit.
Native Windows and OpenBSD acceptance: reports/NATIVE_SVN_TARGETS_2026-09-09.md.

Update/checkout advertise and require `features: ["update_changes"]`.
Their receipt carries `changes: [{path, action}]` (plain A/U/D) alongside
`conflicts`; merged/conflicted work is not reported as a clean incoming edit.
Go passes these notifications to the existing received journal and conflict
reconciliation, without manufacturing CLI text. Older helpers are refused
before these mutations.

Integration checkpoint 2026-09-09 (r1019 base + working delta): URL info/HEAD,
remote lock observations and unmanaged pre-adoption checks now route through C
on Windows opt-in, as do provisioning/attachment/service-WC factories.
`info --inspect-wc` and offline `status --inspect-wc` never stamp ownership;
mutations still require the managed marker. Lifecycle validates URL and its
durable authority before creating that marker (also before resume cleanup).
Native checkout itself does not create it. Linux routing is unchanged.
Remote status returns separate local_lock/repos_lock and against_revision;
ConfirmLock requires matching local and remote tokens/owner/comment. Missing
evidence is an error, never a fabricated empty lock list.

The local status correction for an existing nested unversioned file verifies
its plain node and unversioned/ignored ancestor before returning its status.
Missing paths, unsafe targets and other WC errors still refuse.
Local argv batches now respect both 512 paths and a conservative 12000 UTF-16
unit budget; Windows refuses a complete command over 30000 units before launch.
Distribution downloads accept a separate explicit pinned SSH profile.
This is not complete CLI removal acceptance: DLL/MSI packaging and the
consolidated daemon/VM fault/lifecycle round remain release gates.
Evidence and remaining release gates: reports/NATIVE_SVN_RA_ADAPTER_2026-09-08.md.

Documentation reconciliation: 2026-09-07, source r917. Native WC-local
verbs landed in r911; r913 fixed listing limits, batching and property/status
results. This describes source capabilities, not a new runtime acceptance.

Private client on the public Apache SVN 1.14 C API. Separate process: the
Go daemon does not use cgo. `filees-svn --version` lists implemented verbs.

`recover-commit --wc WC --url URL --commit-id UUID --revision R --targets-stdin`
advertises `recover_plain_add_v1`. It is a narrow local repair of a confirmed
plain nonempty file addition, not a retry of commit. Before update it verifies
WC URL and repository UUID, exact revision marker, changed-path A without copy history and absence
of both local and committed properties. All targets are admitted before any
update. Only a matching incoming text conflict may keep the working text;
existing/tree/property conflicts refuse. Update is depth-empty and pinned to
R; newer BASE is never downgraded. Final status/revision/checksum are checked.
The Windows Go adapter invokes this before the daemon acknowledges its original
publication snapshot. It does not change Unix routing or add a CLI fallback.
`writer_lease_v1` fences every native mutation of an existing WC with an OS
exclusive nonblocking lock on the stable `.svn/filees-native-writer-v1` file.
Marked commits persist their commit-id there before SVN mutation, and clear it
only on successful completion. The file is never replaced or unlinked; process
death releases the OS lock, not the durable provenance. No PID/age heuristic.
The Windows adapter requires this feature before creating a commit intent.

Recovery holds that same lock. Only a matching durable owner plus the exact
remote receipt and all target/identity admission checks permit cleanup of an
orphaned SVN lock/work queue, before pinned repair. Cleanup does not vacuum,
touch timestamps, remove DAV cache or include externals. A live native writer,
foreign/torn owner, or an unowned SVN lock refuses; ordinary mutators cannot
clear a pending owner. Read-only operations remain available. No blind retry.

Real Windows daemon+C crash -> automatic recovery without manual cleanup is
accepted for this plain-add shape, including preservation and later ordinary
publication of an edited generation. C tests also run natively on OpenBSD.
Other metadata shapes, simultaneous external edits and a crash during repair
remain gaps. The lease coordinates cooperating helpers, not arbitrary SVN
tools that bypass it or adversarial filesystem substitution. No power-loss
durability claim follows from process-kill tests. Evidence:
`reports/NATIVE_WRITER_LEASE_RECOVERY_2026-09-09.md`; the preceding conditional
manual-cleanup result remains in `reports/NATIVE_PLAIN_ADD_RECOVERY_2026-09-09.md`.

Linux daemon still uses the helper only for `record-move`; every other
operation stays on distro `svn`. Windows, when `FILEES_NATIVE_SVN` is set,
also routes WC-local verbs (status, add, delete, prop*, cleanup, revert,
resolve) and the supported RA variants described above through the helper.

`commit`, `lock` and `unlock` change server state, and three things about them
are deliberate.

**The message travels through `log_msg_func3`, not the revprop table.**
`svn_client_commit6` has no message parameter, and setting `svn:log` in
`revprop_table` is refused outright (`E195011`, "Standard properties can't be
set explicitly as revision properties"). Measured 2026-09-08: the first version
passed no message at all and every commit succeeded with an empty `svn:log` —
which would have silently emptied the Shouting Commit lane, because
announcements ride in exactly that property. `--revprop svn:log=…` is refused
so the message keeps one source.

**Lock and unlock report each path separately.** Subversion does not fail the
whole call when one path is refused: it notifies and carries on. A verb
reporting only its exit status would turn "somebody else holds this file" into
silence, which is the one thing a reservation must never be. The receipt is a
list of `{path, ok, error}`, and a contested lock comes back as a successful
process containing a failed path.

**Neither steals nor breaks.** Subversion offers both; the inventory in
`concepts/DESKTOP_SVN_CLIENT_SCOPE.md` deliberately does not, and the register
records that force-lock did not become an accepted reservation-migration
mechanism by being inventoried. A capability the product has not accepted has
no business being one keystroke away in the binary that would make it one, so
`--steal` and `--break` are not implemented and are refused as unknown flags.

`commit` uses `svn_depth_empty` with `commit_as_operations`, meaning "these
paths and nothing else". A commit that quietly widened its own scope would
publish work the caller never listed — and FileES builds its batches
deliberately, filtering ignored files and withheld deletions on the way. The
revision comes from the commit callback rather than from a second question to
the server; the `filees:commit-id` marker still works for recognising a
revision after a lost acknowledgement.

An empty commit is not an error: Subversion produces no revision and the
receipt says `null`. The caller decides whether that was expected.

`checkout` and `update` are remote and write to a working copy. Two things
about them are load-bearing.

**Conflicts are reported structurally**, taken from Subversion's notifications
rather than from its printed lines. `pkg/commit/reconcile.go` consumes the
structured native receipt; its text parser remains only for the CLI backend.
Native conflict handling does not depend on Subversion's wording or locale.

**`--force` on checkout is not a convenience.** Measured 2026-09-08 against an
unversioned file colliding with a repository path — the shape FileES meets
whenever the owner points it at a folder that already holds work:

| | without `--force` | with `--force` |
|---|---|---|
| result | `ok`, path becomes a **tree conflict** (`svn status` `D     C`) | `ok`, no conflict |
| the file | broken from the first second | plain local modification (`M`) |
| owner's bytes | kept | kept |

Both keep the bytes, so a check that only compares content cannot tell them
apart. That is why `pkg/client` always passes `--force`, and why removing it
would look harmless right up to the first import.

`update` uses `svn_depth_unknown` for a whole-tree update — "respect what each
directory already records", not "infinity". Passing infinity would quietly
deepen a sparse checkout, turning an update into a download nobody asked for.
Depth is never sticky here: this verb reports history, it does not redefine
what the working copy is.

`checkout` cannot stand on the `.filees` marker, since there is no working copy
yet. It requires an absolute destination with no symlink in its parent chain,
and refuses a destination that is already a working copy — that is a different
operation with a different failure mode, and the caller chooses between them
rather than discovering which one it got.

`log` answers one question with different fields, replacing three CLI
invocations: the shout inbox reads revision and message, the commit-receipt
lookup reads a named revprop (`--revprop`), and move-result recovery reads
changed paths with copyfrom (`--changed-paths`). The target is either `--url`
or `--wc WC -- REL`, exactly one of them, and `--revision A[:B]` is required —
a log with no bound is a way to ask for the whole repository by accident.
Entries are collected before anything is printed, because this protocol
promises one JSON document per invocation and a truncated one is worse than a
late one; bound the size with `--limit`.

Only the three standard revprops plus those named with `--revprop` are
requested. Passing NULL would fetch every revprop the repository holds, which
is somebody else's data.

`cat` is the first remote verb, and the first with no working copy at all. It
writes one repository file to an absolute `--out`, optionally at `--revision`,
and reports the byte count. Two decisions are worth knowing:

- **Keywords are not expanded**, unlike the CLI default. Expansion substitutes
  the URL and revision into the bytes, so the same committed file would differ
  depending on where it was fetched from; this verb carries release material
  verified by signature, and a signature over context-dependent bytes verifies
  nothing. Verified 2026-09-08 against the production server: the output is
  byte-for-byte identical to `svn cat` for real material.
- **It never overwrites, and never leaves a partial.** The download lands on a
  `.part` sibling opened exclusively and is renamed only after the stream
  closes, so an interrupted self-update leaves nothing that looks finished. A
  failed fetch removes the partial, or the next attempt would fail on the
  exclusive open instead of on the real reason.

Since it has no `.filees` marker to stand on, its guard is different in kind:
the target must be a URL and nothing else, and `--out` must be absolute with no
symlink anywhere in its parent chain.

Not routed by the Go adapter yet; `internal/serverinstall/svnfetch` still calls
the CLI and buffers the whole file in memory.

`info` is implemented in the helper but **not yet routed** by the Go adapter.
Its two callers need deciding first: `Revision()` accepts a URL as well as a
working-copy path (client.go:666), and `VerifyCommittedMove` asks for
`dst@BASE` (native_move.go:133) while verifying a commit receipt - a path
where a semantic change deserves its own step. The verb answers only about the
working copy and never contacts the repository: `svn_client_info4` takes its
local branch solely when peg and revision are both NULL or unspecified
(libsvn_client/info.c:353), so WORKING and BASE - the obvious translations of
the CLI's `@BASE` - would silently open an RA session instead. RA verbs (checkout, update, commit, lock,
log, cat) remain on CLI until implemented here. Windows build recipe:
[WINDOWS.md](WINDOWS.md). Native acceptance on Windows is still deferred.

## Contract and trust boundary

`filees-svn record-move --wc WC OLD_REL NEW_REL` records an already
physically moved regular file using `svn_client_move7(metadata_only=TRUE)`.
It does not move user bytes, commit, connect to a server, acquire locks,
infer identity from content, edit wc.db directly, or expose arbitrary SVN
arguments. Additional bounded WC-local verbs are described above. The WC root must be exact and contain a regular `.filees` directory.
This is an accidental-use guard, not authentication.

Relative paths must be canonical and inside the WC; symlinks, metadata
aliases, special files, replacements, switched/conflicted files and
cross-WC moves are rejected. Missing source ancestors are allowed without
recreating them. A source must be a committed missing file, and destination
an unversioned regular file. Destination parents must already be scheduled
or versioned; the daemon adds new parents non-recursively.

Success JSON schema `filees.native-svn/v1` has `ok:true` and state
`scheduled`, or `already_scheduled` only for an exact, reciprocal
SVN moved-from/moved-to pair. Plain delete/add is not accepted as a receipt.
Failure returns nonzero and `ok:false, errors:[{code,message}]` containing
Apache/APR numeric errors. The adapter validates success receipts and caps execution at 30 seconds.
Record-move output and stderr are limited to 64 KiB; WC-local JSON stdout
allows 8 MiB after r913, with path batches of 512. On failure the current
Go adapter embeds errors[] as text rather than preserving typed SVN/APR
codes (M46). Structured classification in the existing errcat and retaining
unknown diagnostics through GUI are still open.
This process protocol is not the GUI IPC or the final i18n error catalog.

## Daemon integration and recovery

Linux and Windows opt in with the absolute `FILEES_NATIVE_SVN` executable
path. A missing executable while enabled is an error, not feature
auto-disable. The main repository factory passes the adapter into the
existing pipeline. Extra WC-local verbs are ignored on Linux even when
the helper binary contains them.

- The watcher persists Linux device/inode/birth-time identity, accepts only
  unique regular-file matches with one hard link, and detects edited moves
  independently of equal bytes. Missing/ambiguous identity holds publication.
  Known different identities do not assert continuity.
- Pending move intent is saved before native mutation, and scheduling state
  after it, using the existing commit cache. Without durable cache no move.
- A service mutex serializes event acceptance, poll/update and publication;
  the adapter shares the existing CLI operation mutex.
- Existing publication/passport locking still applies. If SVN acquires a
  lock for a missing source but fails its chmod, success requires matching
  local AND server tokens and the exact requested comment, not a force retry.
- Normal commit records copyfrom and source deletion together. Existing
  properties and first-committer continuity survive. Empty old directory
  deletion is deferred until contained file moves have committed.
- Restart after a lost native reply recognizes the exact scheduled pair.
  Lost commit acknowledgement requires actual committed copyfrom/delete
  history, not just a clean status. Inconclusive history holds the intent.
- An unscheduled rename chain can collapse to its original source. A further
  rename after scheduling is held for review; no half-chain publication.
  A genuinely unpublished file becomes a new addition only after SVN status
  confirms no source object. A stale Added cache entry cannot erase ancestry.

No error or ambiguity falls back to delete/add in the enabled path.
There is no automatic revert, rollback or force takeover. The only automatic
cleanup is the receipt/provenance-gated recovery described above.
Concurrent external WC writers and adversarial filesystem substitution are
not protected by the native lease; this is not a security boundary.
Whole-directory identity is not implemented: contained files may move
individually, but directory-object continuity is not claimed.

## Build and tests

Build tools belong on the developer host: coherent SVN 1.14/APR headers
and libraries, C compiler and CMake >= 3.18. Nothing is downloaded or
installed by the build scripts.

```sh
DIST=/tmp/filees-native-build sh packaging/build-native-svn.sh
FILEES_SVN_PROBE=/tmp/filees-native-build/filees-svn \
  go test -race -count=1 -buildvcs=false -tags=native_svn_probe \
  ./native/filees-svn ./pkg/client ./pkg/commit ./pkg/watcher
```

The historical `FILEES_SVN_PROBE` variable and `native_svn_probe` tag
select disposable harnesses only; they do not enable the daemon.
The C harness keeps `--disposable-wc` with `.filees-native-probe` marker.
Real integration tests use `--wc` on temporary FileES WCs and real SVN.

For a private SDK set `FILEES_CMAKE`, `SVN_INCLUDE_DIR`,
`APR_INCLUDE_DIR`, `SVN_CLIENT_LIBRARY`, `SVN_WC_LIBRARY`,
`SVN_SUBR_LIBRARY`, `APR_LIBRARY` to coherent absolute paths.
Use `FILEES_BUILD_NATIVE_SVN=1` with `packaging/build-pair.sh` to
also build the helper; ordinary builds/installers are unchanged.
Runtime still needs the matching SVN/APR shared-library dependency closure.

Windows preparation is a recipe, NOT acceptance: configure the same
CMake source with a coherent x64 SVN/APR SDK, build the wide-argument entry
point and place matching DLLs beside `filees-svn.exe`. Set
`FILEES_PROBE_SVN` and `FILEES_PROBE_SVNADMIN` for a separate CLI if
needed, then run `./native/filees-svn` with the same tag and native binary.
Symlink fixture capability is required. Linux-only integration tests do not
substitute for Windows Unicode, case-only moves, long paths or DLL testing.
The current daemon ignores opt-in on platforms other than Linux and Windows.

## Rollout and withdrawal

Deploy daemon, GUI and helper as the desktop user; retain the previous pair.
Enable the explicit path in a user-service drop-in, then restart normally.
Observe `native-move scheduled/already_scheduled` logs and queue progress.
A successful lab smoke starts observation under workload, not proof of
long-term reliability or acceptance of every application save pattern.

Before disabling/removing the helper, drain or explicitly reconcile native
pending intents and check SVN status. The new daemon holds native intents
if the helper is disabled; reverting blindly to an older daemon loses that
guard. Do not use delete/add, revert or cache deletion as a rollback shortcut.

Remaining gates: sustained Linux workload, abrupt kill during library
mutation, external-writer races/TOCTOU, unsupported filesystem identity,
scheduled rename-chain recovery UI, Windows native integration and runtime
packaging/licensing, oldest supported Linux runtime. Broker path-owner transport is implemented separately in r910; its rollout
and full group autolock remain open.

The isolated historical result remains in
[the r896 report](../../reports/NATIVE_SVN_PROBE_2026-09-06.md).
Linux r898 deployment and first real two-realm move are recorded in
[the live report](../../reports/NATIVE_SVN_LINUX_LIVE_2026-09-06.md).
API: [Apache SVN move7](https://subversion.apache.org/docs/api/1.14/group__Move.html).
