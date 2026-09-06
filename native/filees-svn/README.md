# FileES native SVN client — Linux alpha integration

This is the first, deliberately narrow verb of a private client built on
the public Apache SVN 1.14 C API. It is a separate process: the Go daemon
does not use cgo. Stock SVN CLI still handles every other operation.
The owner authorized Linux live integration after the isolated r896 probe;
Windows native acceptance is deferred, not silently inferred from Linux.

## Contract and trust boundary

`filees-svn record-move --wc WC OLD_REL NEW_REL` records an already
physically moved regular file using `svn_client_move7(metadata_only=TRUE)`.
It does not move user bytes, commit, connect to a server, acquire locks,
infer identity from content, edit wc.db directly, or provide general SVN
verbs. The WC root must be exact and contain a regular `.filees` directory.
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
Apache/APR numeric errors. The adapter validates receipts, limits output to
64 KiB per stream and execution to at most 30 seconds. Failures enter the
existing errmap/log path; unknown cases retain alpha raw diagnostics.
This process protocol is not the GUI IPC or the final i18n error catalog.

## Daemon integration and recovery

Only Linux opts in with the absolute `FILEES_NATIVE_SVN` executable path.
A missing executable while enabled is an error, not feature auto-disable.
The main repository factory passes the adapter into the existing pipeline.

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
There is no automatic revert, cleanup, rollback or force takeover.
Concurrent external WC writers and adversarial filesystem substitution are
not protected by the in-process mutex; this is not a security boundary.
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
The current daemon intentionally ignores opt-in on non-Linux platforms.

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
packaging/licensing, oldest supported Linux runtime. This does not finish
path-owner broker transport or full group autolock.

The isolated historical result remains in
[the r896 report](../../reports/NATIVE_SVN_PROBE_2026-09-06.md).
API: [Apache SVN move7](https://subversion.apache.org/docs/api/1.14/group__Move.html).

