<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="branded-assets/filees-space-svg-pack/filees-space-monochrome-white.svg">
    <img src="branded-assets/filees-space-svg-pack/filees-space-monochrome.svg" alt="FileES" width="360">
  </picture>
</p>

# FileES

*English | [Polski](README.pl.md)*

Sync and share system built on top of Apache Subversion, server side targeting OpenBSD.

> ## ⚠️ Work in progress — not for use
>
> **This is not a release. This is not a beta. This is a work-in-progress draft of a work-in-progress system, published only so the code can be looked at.**
>
> - There is no stable version, no support and no guarantees of any kind — neither correctness, nor security, nor data integrity.
> - On-disk formats, protocols, command names and schemas change without notice and without a migration path.
> - Do not deploy this on a production machine and do not trust it with data whose loss would be a problem.
> - The code is under continuous audit and review; known defects can stay open for weeks, because the priority is the project, not polish.
> - Internal design documents, concepts and audit reports are **not published**. This repository is a filtered mirror of an SVN repository and contains code, the manual and readme files only.
>
> Issues and pull requests are not expected and may go unanswered.

Daemon that synchronizes local directories with an SVN repository. Built for teams working with binary files (artwork, 3D models, project assets). SVN is used here as a transport layer and storage backend — version-control semantics are secondary.

Target UX: a tray automaton that invisibly keeps files synchronized with the server. The user should not need to know that SVN runs underneath.

---

## Documentation

- **[manual.filees.space](https://manual.filees.space)** — the full HTML manual, PL/EN, live (origin). The landing page is the language switch.
- **[manual/index.html](manual/index.html)** — the same content mirrored in this repository (chapters under
  `manual/assets/pl/` and `manual/assets/en/`); the origin at `manual.filees.space` is the authoritative version.
- **[docs/man/](docs/man/)** — `mandoc` pages for the server-side tools (`man filees`, `man filees-admin`, `man filees.conf`).
- **[USERGUIDE.md](USERGUIDE.md)** — a shorter user guide.
- **[manual-filees.html](manual-filees.html)** — a redirect stub to `manual/`, kept so old links don't die.

**Desktop GUI:** `cmd/filees-gui` (Fyne + zenity/yad on Linux, WinForms on
Windows) is **deprecated** and receives only blocking bug fixes. The target,
actively developed client is `cmd/filees-gui-wails` (Wails/WebView) — the
full rationale and the conditions for retiring the old stack are in the
[GUI Tray](#gui-tray) section below and in `concepts/WAILS_GUI_FORK.md`.

The current desktop client supports, on Windows, fully joining another
installation to an existing realm, listing every realm repository in
Settings, and a selective first checkout. A realm grant does not mean every
repository is automatically fetched onto every device. WC lifecycle,
journal and alias-projection details are in
`reports/WINDOWS_REALM_JOIN_WC_AND_JOURNAL_FIX_BLOCK_2026-08-10.md`.

---

## Quality

The neutral CI quality gate is `make verify`: the full Go test suite, selected race tests, `go vet`, and a local SVN recovery smoke test. The smoke test itself runs `scripts/svn-recovery-smoke.sh` — it creates a temporary SVN repository and needs no network access.

---

## Requirements

- Go 1.25+
- SVN client (`svn`) available on `PATH`
- OpenSSH client (`ssh`) and an active FileES installation identity
- Access through the system `sshd` to tunneled `svnserve -t`; a listening
  `svnserve --daemon` is not supported

---

## Building

```bash
go build -o filees ./cmd/filees
```

---

## Configuration

The daemon looks for a `config.json` file in the working directory. SSH
transport belongs to the client installation, not to a single repository:

```json
{
  "transport": {
    "identity_file": "/home/user/.local/share/filees/identity/id_ed25519",
    "known_hosts": "/home/user/.local/share/filees/known_hosts"
  },
  "update": {
    "enabled": true,
    "repo_url": "https://releases.example/FILEES-BIN",
    "channel": "stable",
    "component": "desktop",
    "platform": "linux-amd64",
    "state_path": "/home/user/.local/state/filees/update.json",
    "stage_root": "/home/user/.local/state/filees/update-stage"
  },
  "repositories": [
    {
      "id":              "projectA",
      "repo_url":        "svn+ssh://_filees-client@server/repo/trunk",
      "local_path":      "/home/user/projectA",
      "commit_interval": "1m",
      "watch_interval":  "30s",
      "poll_interval":   "30s",
      "max_batch_files": 100,
      "max_batch_mib": 512,
      "backlog_flush_mib": 1024,
      "shutdown_commit_timeout": "10m",
      "lock_first":      false,
      "edit_passports":  false,
      "edit_passport_ttl": "15m",
      "edit_passport_heartbeat": "5m",
      "edit_passport_max_session": "24h",
      "edit_passport_close_grace": "5m",
      "shout_patterns":  ["\\.psd$", "\\.blend$", "\\.obj$"],
      "rate_limit_shout":"5m",
      "commit_tiers": [
        {"max_mb": 1,  "interval": "2m"},
        {"max_mb": 10, "interval": "5m"},
        {"max_mb": 50, "interval": "15m"},
        {"max_mb": 0,  "interval": "24h"}
      ]
    }
  ]
}
```

| Field               | Description |
|--------------------|------|
| `id`               | Unique repo identifier (used in logs and state paths) |
| `transport.identity_file` | Absolute path to the Ed25519 key created during activation |
| `transport.known_hosts` | Absolute path to the pinned service host key |
| `update.enabled` | Enables the signed client updater; disabled by default |
| `update.repo_url` | URL of the SVN/HTTPS release repository, without password, query or fragment |
| `update.channel` | Release channel, defaults to `stable` |
| `update.state_path` | Private, absolute path to the durable high-water mark file |
| `update.stage_root` | Private, absolute path to the verified staging directory |
| `repo_url`         | `svn+ssh://_filees-client@host/...` URL; other transports are rejected |
| `local_path`       | Absolute path to the working copy |
| `commit_interval`  | Commit window (e.g. `1m`, `30s`) |
| `watch_interval`   | Filesystem scan interval |
| `poll_interval`    | How often to check the server HEAD and pull changes (`svn update`); defaults to `30s` |
| `max_batch_files`  | Max number of files in a single commit |
| `max_batch_mib`    | Target max size of a single commit in MiB; a single larger file gets its own batch |
| `backlog_flush_mib` | Backlog threshold in MiB that forces a commit without waiting for the normal interval |
| `shutdown_commit_timeout` | Max time to fully drain staging during a controlled shutdown |
| `lock_first`       | If `true` — tries `svn lock` before committing |
| `edit_passports`   | Only for manually configured legacy/dev repositories: enables edit passports. For server-provisioned repositories this field is ignored in favor of the canonical `editing_policy` |
| `edit_passport_ttl` | Validity of a single passport renewal; defaults to `15m` |
| `edit_passport_heartbeat` | Renewal interval, shorter than the TTL; defaults to `5m` |
| `edit_passport_max_session` | Non-extendable session limit; defaults to `24h` |
| `edit_passport_close_grace` | Required quiet period after a confirmed commit; defaults to `5m` |
| `shout_patterns`   | Regex patterns; matching files trigger a notification (ticket) |
| `rate_limit_shout` | Minimum interval between notifications |
| `commit_tiers`     | Size-adaptive intervals (list, ascending by `max_mb`); omitted = `commit_interval` only |

**`commit_tiers`** — each entry is `{"max_mb": N, "interval": "Xm"}`. The daemon checks the total size of files in the current batch and applies the minimum interval of the matching tier. `max_mb: 0` is the catch-all (last tier). Example: batches < 1 MiB every 2 min, 1–10 MiB every 5 min, 10–50 MiB every 15 min, > 50 MiB every 24h.

Durations use Go's format: `30s`, `5m`, `1h`.

In the normal product flow, the owner changes a repository's policy in
Settings through the **Edit policy** action. The server stores it once in
the canonical record and projects it identically to every client; the only
value sent over the wire is `lock_required`, and a missing field means
ordinary editing. The full lifecycle and multi-client guarantees are
described in chapter
[2.5 Edit Passports](manual/assets/en/user-guide.html#ch2-passports).

Every `local_path` must be an absolute path. Repository identifiers must be unique, and local roots may not be identical to, or nested inside, one another. Validation resolves symlinks on existing directories and fails daemon startup hard, before any `.filees` state is created.

An SVN password given in the configuration is stripped from `trace` logs. The SVN 1.14 client offers no secure stdin password input, so until the move to the target SSH keys the password is still passed to the `svn` process as an argument. On shared hosts, prefer key-based transport or a system account with restricted access to the process list.

---

## Running

```bash
./filees                        # runs the daemon (default)
./filees daemon                 # explicit daemon start
./filees --config path/to/config.json
```

Log level via an environment variable:

```bash
FILEES_LOG=debug ./filees
FILEES_LOG=trace ./filees   # including svn invocations
```

Available levels: `silent`, `error`, `warn`, `info` (default), `debug`, `trace`.

Optional log prefix:

```bash
FILEES_LOG_PREFIX=myhost ./filees
```

---

## CLI commands

The daemon listens on a Unix socket (`$XDG_RUNTIME_DIR/filees.sock` or `~/.filees/daemon.sock`). All subcommands talk to the running daemon over that socket — they never read `.filees/` files directly nor invoke `svn`.

```bash
filees status               # status of all repositories
filees lock   <file>...     # acquire an SVN lock
filees unlock <file>...     # release an SVN lock
filees log [N]              # last N entries from the error log (default 20)
filees help
```

`lock` and `unlock` accept multiple files at once and group them by repository automatically. Paths may be relative — the daemon converts them to absolute paths and verifies they lie inside a working copy.

---

## GUI Tray

`filees-gui` is a separate process and a thin UX layer over the public IPC contract. The GUI is not part of the daemon, knows nothing about SVN, and does not take on responsibility for synchronization. A crash of the GUI process alone does not kill the daemon, while the explicit **Close FileES** user action controllably stops the daemon and GUI as one client stack.

### Hard GUI–daemon boundary

The GUI may import only:

- `pkg/ipcclient` — transport and typed IPC operations,
- `pkg/contract/v1` — public DTOs, states, events and capabilities,
- its own presentation packages and system-tray integration.

The GUI may not:

- import `watcher`, `commit`, `client`, `ipcserver`, `errmap` or any other engine package,
- invoke `svn` or modify a working copy on the daemon's behalf,
- read `config.json`, `.filees/`, caches, manifests or error logs directly,
- reconstruct state from logs or from the text details of an error,
- call commands the daemon has not advertised in `capabilities`.

The only exception outside IPC is local, UX-owned actions, e.g. opening a repository's directory in a file manager. They may never change sync state.

### Startup model

- the daemon runs independently, ideally as a user service,
- `filees-gui` may start with the graphical session and connects to the existing socket,
- no daemon is a normal UX state, not a GUI failure,
- the GUI retries the connection with bounded backoff, e.g. `1s → 2s → 5s → 10s → 30s`,
- **Restart FileES…** and **Close FileES…** are available only once
  `system.restart`/`system.shutdown` are advertised; both operations cover
  the daemon and the GUI.

Once connected, the GUI performs:

1. `system.hello` and stores the capabilities,
2. `events.subscribe`, if the capability is available,
3. `system.status`, `repo.list` and `repo.status` for every repository,
4. periodic snapshot refresh as a self-healing mechanism.

The subscription is set up before snapshots are fetched, so a change happening during initialization is not missed; an event received in that window can at most trigger an extra refresh. The `repo.status` snapshot is the only authoritative source of state. Events are a signal to refresh the corresponding snapshot quickly — the GUI does not build durable state purely by layering events. A full resync runs after a reconnect, a `sequence` gap, or an invalid event.

### Tray state model

Icon state is an aggregate over all repositories. A fixed priority order applies, so a more severe state is never masked by a healthy repository:

| Priority | Icon state | Condition |
|-----------|------------|---------|
| 1 | disconnected | daemon unreachable or protocol mismatch |
| 2 | attention required | `interaction_required`, `degraded`, a conflict, or an error requiring action |
| 3 | offline | at least one repository is offline |
| 4 | work in progress | a `current_operation` exists, or a transitional state |
| 5 | ready | all repositories are active and online |

An unknown state or an unknown enum value is presented as a safe "unknown state" and triggers a refresh, never a GUI crash.

Aggregation priority is: `disconnected > error > offline > busy > active`. While disconnected, the last snapshot may remain visible in the menu, but only marked as stale.

### MVP menu

The tray menu contains:

- the aggregated daemon state and the time of the last successful refresh,
- a list of repositories with state, connectivity, revision and pending-change count,
- "Add folder to FileES…" next to a server that allows this client to create repositories,
- a global header item "File reservation list…", active only when at least
  one reservation is locally visible; it opens a native, multi-server lock
  list built from locally attached WCs,
- "Open directory" for each repository,
- `Lock…` and `Unlock…` with file selection scoped to a given repository,
- directly in the server submenu (above the expandable folder), **Detach
  folder "&lt;name&gt;"…** for an optional WC, and a separate, double-confirmed
  **Permanently detach "&lt;name&gt;"…** for an own-realm repository,
- in the "FileES Settings" window, when the daemon advertises the capability:
  **Visibility…** (toggles the visibility of one's own realm entry in the
  private recipient directory), **Guest permissions** per repository
  (current state plus granting/revoking `r`/`rw` to a visible realm),
  **Public shares** per own repository (list, create, edit, revoke and
  delete a channel), and **Restore from archive…** for a selected repository
  row (loads a previously exported SVN dump as a new generation of that
  repository, through the same mechanism as `filees-rotate`),
- one global **Log** submenu, merging activity and errors newest-first; the
  tray shows up to 12 aggregated entries, and **Open log…** opens the full
  available snapshot in a native window; the preview translates time to
  "just now", "N minutes ago", the hour, "yesterday" or "N days ago", while
  the full view uses `dd:mm:yy hh:mm`; errors are explicitly highlighted,
- "Reconnect" when the daemon is unreachable,
- an "Update — coming soon" placeholder when no release is advertised,
- "Restart FileES…" and "Close FileES…".

Items depending on mutating commands are built strictly from capabilities and a fresh snapshot. The GUI currently supports, among others, `events.subscribe`, `repo.create_request`, `repo.attach_intent`, `repo.attach_approve`, `repo.locate`, `repo.detach`, `repo.delete`, `repo.load_dump`, `repo.lifecycle_status`, `repo.activity`, `repo.grant_access`, `repo.revoke_access`, `repo.public_share_list`, `repo.public_share_create`, `repo.public_share_update`, `repo.public_share_revoke`, `repo.public_share_delete`, `repo.lock`, `repo.unlock`, `repo.reservation_list`, `repo.reservation_release`, `realm.alias_claim`, `realm.grant_recipients`, `realm.set_visibility`, `system.restart`, `system.shutdown`, `error.list`, plus the dynamic `update.status`, `update.plan` and `update.apply`. Update capabilities appear only with a complete, signed update service registered. `Pause`, `Sync now`, publishing changes and interactive conflict decisions stay hidden until the daemon implements and advertises them.

Joining another installation to an existing realm is authorized by the
administrator when the ticket is created (`--join-realm-alias`). After
activation, Settings shows the full realm-repository projection, locally
`attached` or `unattached`. Windows lets the user select multiple
unattached rows and choose **Connect**; a separate local-directory picker
appears for each repo. Once accepted, the row immediately shows the chosen
path and `connecting…` until the daemon confirms the first checkout. Linux
uses the same lifecycle for a single selected row.

Creating a repository is an ordinary user operation, with no console involved:

1. In the relevant server's submenu, choose "Add folder to FileES…". The action is hidden for a read-only client, a server without permission, or a stale connection.
2. Point at an existing local folder in the native Linux/Windows picker.
3. Accept the name derived from the folder name, or type your own.
4. Review server, folder and `rw` access in the summary, and choose "Create".

The GUI re-checks freshness and permissions immediately before the IPC request. The daemon canonicalizes the path, rejects overlapping roots, and durably records the operation before responding. Further server-side creation, content import, the first commit and repository attachment happen asynchronously; accepting the operation yields the first notification ("Repository creation started"). If the repo already exists on the server but `INITIAL_COMMIT` is still in progress, the projection keeps the chosen local path and shows **initial import in progress** instead of a false **not locally pinned**; runtime and mutating actions stay blocked until the import succeeds. The GUI then polls `repo.lifecycle_status` by `operation_id` (every 3 s by default, up to 15 min) and shows a second notification with the real outcome: "Repository created", or, if any stage fails (e.g. `STORAGE_INSUFFICIENT` when the server is out of space), an error message with the exact reason. A repository that does not make it through this pipeline never receives a registered `RepoID`, so it never shows up in repository state or in `error.list` — the second notification is the only place such a failure is visible.

Detaching has two disjoint contracts:

- **Detach folder…** stops the runtime, removes only `.svn` and `.filees`
  from the local root, and leaves all user data as a plain folder; it also
  works for an offline repo, and a durable tombstone blocks reattaching the
  old `config.json` entry. The final directory sync uses the platform's
  `durable.SyncDirectory`: it preserves `fsync` on POSIX and does not hit
  `ERROR_ACCESS_DENIED` on Windows. A subsequent **Connect** requires a new
  or empty target; a rejected attach is shown both modally and as a
  notification;
- **Permanently detach…** requires two separate confirmations, server-side
  ownership, and repo-administration capability. For retention `X>0`, the
  server creates and verifies a full dump, immediately removes the FSFS
  repository, and keeps only the dump for `X` days. The default `X=30`
  stores the dump and a SHA-256 manifest under
  `results_root/deleted-repositories`; the worker's result carries the
  exact `retain_until`. For `X=0`, it removes the FSFS repository
  immediately without creating a dump.

  Server success, issuing the archive capability, and local WC-metadata
  removal are three independent, durable boundaries. After a successful
  `DELETE_REPOSITORY`, the daemon records `retain_until`, then
  independently attempts to issue a keyed `.fkr` bundle and remove
  `.svn`/`.filees`. An older worker without a recovery ticket does not
  block the local cleanup, and a `wc.db` lock is not reported as a server
  failure. The repo stays in the projection as deleted, with a separate
  pending state for each of the two outcomes; only the local cleanup is
  automatically retried, with no network traffic. With positive retention
  the panel shows a separate archive group, a countdown to zero, and a
  download action; with zero retention it shows no dead action.

A repository with `attachment_policy=required` offers none of these actions.
The lifecycle is durable and resumable across restarts.

The root of an active WC is a tracked artifact. Before starting the
pipeline, the daemon checks `.svn`, the exact URL, and a FileES identity
marker. A missing or swapped root yields
`interaction_required / working_copy_missing`; FileES never recreates an
empty directory nor reports it as a healthy revision 0. Settings then shows
**Locate copy**, which, via `repo.locate`, accepts an existing, relocated WC
without a checkout and without discarding local changes. On Windows, the
active root is additionally protected by a handle blocking external
rename/delete; a controlled FileES operation releases that handle first.

**Realm visibility and grants** work through two separate actions in the
"FileES Settings" window (Linux: `yad` radiolist; Windows: PowerShell
`DataGridView`). **Visibility…** toggles one's own realm entry in the
private recipient directory between hidden and visible — a realm must be
visible before another realm can pick it as a grant recipient; toggling
never reveals repositories or existing access. **Guest permissions**,
available per repository row, opens the union of currently visible realms
and hidden realms with an active grant. The table shows current `r`/`rw` or
no access, and lets you grant read-only, read-write, or revoke access;
hiding a realm does not make an existing grant unmanageable. Every change
requires confirmation and immediately regenerates `data-authz` and
`view.json` on every affected installation.

**Public shares** is an owner-only action per repository. The window shows
active and revoked channels along with their address, source, recipients,
password protection and revision policy. A channel can be created, edited,
revoked or deleted. On create/update the user picks the root or a subfolder
of the local working copy; the GUI recursively builds a map of up to 4096
regular files with their sizes, skips `.svn` and `.filees`, rejects
symlinks, and keeps a stable `public_id` for paths already present in the
edited channel. An empty folder is a legal placeholder; later files require
an explicit channel update. The public page renders exclusively from the
safe map projection, as a default-collapsed tree in a "Details"-style view:
name, icon and type description, size, and single-file download. A
selection, a folder, or the whole share can also be downloaded as a bounded
ZIP. The view uses the `filees:space` identity; it uses no JavaScript and
no thumbnails. A closed channel uses per-email-address tokens; an open
channel may be passwordless or protected with an Argon2id password.
Plaintext is hashed before the IPC control plane and never reaches the SVN
ticket. Editing may keep the existing server-side verifier without ever
sending it back to the client. A zero revision follows HEAD; a positive
number sets `do-not-follow`.

**Restore from archive…** (`load_dump`, IPC `repo.load_dump`, ticket
`LOAD_REPOSITORY_DUMP`) is available for a selected repository row next to
Detach/Permanently detach. It loads a previously exported SVN dump as a new
generation of the repository, through the same staging/verify/atomic-swap
mechanism `filees-rotate` uses internally (`manual/assets/en/administration.html`
§4.7) — the worker locates the uploaded dump itself; the client sends
neither a path nor a revision range. The first release ships with no
options dialog: filtering always applies the server's current ignore
policy, and the full source history is preserved.

### File reservations

The header item **File reservation list…** opens a native Linux/Windows
window with active SVN locks found across every working copy locally
attached to active servers. A row carries server, working-copy name,
relative path, owner alias, and local creation time in `HH:MM DD-MM-YYYY`
format. The list is ordered by working copy, then by path. This is not an
administrative listing of every lock on the whole server — a repository
with no local WC is not observable in this view.

Selecting a row and choosing **Release** always prompts for confirmation.
When that WC has local changes, or the lock corresponds to an active edit
passport, the dialog explicitly warns about unsaved data and requires a
deliberate confirmation. The GUI makes no attempt to detect editor handles:
many programs save via an atomic file swap, so such a check would be only
apparent protection. A reservation tied to a passport active on another
device is informational only: its action column shows a **Request release
(coming soon)** placeholder and it cannot be released from this client. The
**Release all** button covers only reservations this client can release,
requires one confirmation, and executes each operation with its own token.
A single-release request also carries a token from the list; the daemon
re-reads the state and rejects a changed or stale row before calling SVN.

The owner alias is a durable realm identity, not an email address or a UID.
**Set permanent alias…** is offered only to a fresh, empty realm. A client
joining an existing realm inherits its canonical alias on the first
projection and cannot rename it. A missing alias on an already-projected
repository means an incomplete projection, not a task for the user;
**Visibility…** and locks are then blocked with an explicit message until
the server reconciles.

### Signed desktop client updates

The updater is opt-in and fail-closed. A production build must embed a
release public key and its `key_id`; configuration cannot swap the key nor
disable signature checks. The daemon advertises `update.status`,
`update.plan` and `update.apply` only once a complete update service is
registered.

A verified v2 envelope binds `release_id`, a monotonic `sequence` and
`security_epoch`, an expiry, the component, the platform, and an artifact
manifest. The client checks the OpenBSD signify format internally via
Ed25519, then the bundle's exact size and SHA-256. A durable high-water
mark blocks downgrades, lowering the security epoch, and forking the same
sequence.

The GUI shows an "Update available" badge. "Show what will change…" is a
dry run, and "Update and restart…" re-resolves the signed release, shows a
native confirmation, and runs the existing installer. Linux preserves
configuration and disables restart/autostart inside the script; on
success, the GUI requests a restart of the whole stack, exits, releases the
single-instance lock, and only then launches the new binary. The
publishing procedure: `tools/RELEASE_PUBLISHING.md`.

### Notifications

System notifications are secondary to the state shown in the menu. The MVP shows them for new errors, a repository transitioning into a state requiring attention, connectivity loss/recovery, and completion of a user-relevant operation. Repeated events are grouped and rate-limited. Notifications stay informational; safe click-through activation requires a separate native receiver and may never perform a mutating operation.

### Module map

```text
cmd/filees/              daemon, CLI and client composition root
cmd/filees-gui/          composition root and lifecycle of the presentation layer
cmd/filees-gui-wails/    target Wails/WebView client for the same IPC projection
android/                 mobile Kotlin client, gomobile bridge through pkg/mobileclient
internal/gui/            model, actions, tray, platform and notifications
pkg/contract/v1/         GUI/CLI <-> daemon IPC contract
pkg/ipcclient,ipcserver/ local control-plane transport
contracttests/           shared conformance gate for envelopes, capabilities and IPC E2E

pkg/clientview/          strict projection of installation state from the service repo
pkg/localrepo/           durable lifecycle of local WC attachments
pkg/provisioning/        create/attach/initial-commit state machine
pkg/reposupervisor/      running and reconciling multiple repositories
pkg/watcher,commit/      scanning, batching, update and SVN publishing
pkg/passport/            needs-lock, lease, fencing and edit-policy migrations
pkg/shout/               release marker in svn:log and a local message inbox
pkg/errcat,errmap/       shared error vocabulary and diagnostic classification

pkg/control/v1/          signed client -> worker requests
pkg/whale/v1/             generation canon and windowed Whale framing
pkg/whaleclient/         durable actor/spool and pinned SSH PUT/GET transport
pkg/repoworker/          authoritative repositories, grants and projections
internal/servertool/     forced-command entrypoints, lease/revoke supervisor and server operations
internal/whaleworker/    PUT, aware GET, seekable cache and svnmucc file://
pkg/onboarding,activation/ activation, identity and the service repo
cmd/filees-service-wc-corrector/ owner/group correction for the service WC before a ticket

internal/mobileworker/   mobile read and capture under mobile-uploads, including UPLOAD_TREE
cmd/filees-public-authority/ public shares and their own trust boundary
public-shares/           channel, gateway, web projection, depositor OTP, intake waiting room
internal/uploadworker/   waiting room and AV reaper for public-upload quarantine
pkg/avscan/              AV classification (clamscan/clamdscan) and the EICAR test signature
internal/clientupdate/   signed desktop updater
internal/serverinstall/  core of the manifest-based server installer
internal/release*/       envelopes, signatures and artifact publishing
```

The tray library is `fyne.io/systray`, isolated as an adapter in `internal/gui/tray`. Its API must not leak into application logic or the contract. MVP covers Linux (SNI; GNOME requires the AppIndicator/SNI extension) and Windows 10+. Detailed platform decisions live in `gui-assumptions.md`. **`cmd/filees-gui` (this Fyne+zenity/yad/WinForms renderer) is deprecated** — decision below — and now receives only blocking bug fixes, no new features.

**The target GUI is `cmd/filees-gui-wails`**, pinned to Wails
`v3.0.0-beta.6`. The decision was made 2026-08-26 (r603): what started as a
WebView experiment produced such an unprecedented UX improvement — a full,
persistent window instead of a series of native dialogs, a consistent
layout and theme on Windows and Linux, a live projection with no manual
refresh — that any other decision was not realistic. So far we have not
run into any blocking weakness of Wails itself in beta; the bugs closed
along the way (e.g. prompt bindings carrying identifiers from `go test`
instead of from the build) were our own mistakes, not a framework
limitation. Before formally cutting the old stack, what remains is
tidying up the UI, which grew fairly spontaneously during the r576–r603
week — tidying, not a rewrite — and closing parity gaps (a full admin tray
menu, single-instance, a local PIN). The conditions for cutting
Fyne+zenity/yad and the architecture details: `concepts/WAILS_GUI_FORK.md`
§0 and §7.

Wails does not introduce a second client model: it runs the same
`internal/gui/app`, talks exclusively through `pkg/ipcclient`, and the
WebView renders the received projection and returns intents. It has its
own EXE, a static frontend with no Node/Vite, and `Snapshot`, `Refresh` and
`Reconnect`. The `Open`, `Lock` and `Release` actions go through the same
`internal/gui/actions` as Fyne; JavaScript never calls IPC directly. The
Windows window is frameless, and hiding the WebView scrollbar does not
disable scrolling. Active locks are part of the projection; an inline
release passes only an opaque ID, and the fencing token stays in Go. The
Wails tray keeps the process alive after the window is hidden and shows
state plus repository and lock counts. The `FileES` submenu routes restart
and shutdown of the whole daemon+GUI pair to a shared controller; there is
no longer a local action that ends only the renderer.

### Implementation staging

1. **Tray-less core** — `internal/gui/app`, the `DaemonClient` interface, a single state loop, init, reconnect, resync, debounce, plus an architectural and a unit test with no GUI.
2. **Tray adapter** — `internal/gui/tray` on `fyne.io/systray`, five icons, a menu rendered from a `ViewModel`, and user intents with no direct IPC access.
3. **Platform integrations** — 3A: clean interfaces and a fake backend; 3B: Linux (opening directories, pickers, notifications, XDG autostart); 3C: Windows equivalents; 3D: the non-blocking `tray.Intent` controller that coordinates the platform and the `DaemonClient` boundary without importing an IPC implementation.
4. **MVP integration and acceptance** — `cmd/filees-gui`, metadata and packaging of existing assets, app ↔ fake-IPC tests, manual tests on both platforms, and verification of daemon restart, a slow GUI, and multiple repositories.

Stages 1 and 2 are complete. The `fyne.io/systray` adapter is decoupled from IPC and the contract by a `ViewModel`, has five embedded icons (PNG/ICO), a deterministic menu model, user intents, and renderer and import-boundary tests. The detailed scope of the following stages and the checklist live in `gui-assumptions.md`.

Stage 3A is complete: `internal/gui/platform` defines clean system interfaces, classification of unavailability and operational errors, and a concurrency-safe fake backend. The package depends on neither the tray, the app, the IPC contract nor the engine; an architectural test guards the boundary.

Stage 3B is complete: the Linux adapter provides `xdg-open`, file and directory selection via Zenity/KDialog, grouped and rate-limited `notify-send` notifications, and an atomic XDG autostart with `Hidden=true` support. Desktop calls are injected and tested without opening real windows.

Stage 3C is complete implementation-wise: the Windows adapter covers Explorer, PowerShell/WinForms file and directory pickers, `ToastGeneric`, and HKCU autostart. Processes and the registry are injected; quoting follows Windows rules, and notifications require FileES's own AUMID, registered by the Stage 4 package. The picker, prompts and notifications run PowerShell with no visible console window and set per-monitor DPI awareness before creating a window. Native acceptance testing remains part of every Windows release checklist.

Stage 3D is complete: `internal/gui/actions` handles tray intents non-blockingly, re-checks model and repository freshness after a picker interaction, validates paths before IPC, and serializes lock/unlock within a single repository. The controller owns the lifecycle of its own tasks and single-flight for directory opening. An architectural test protects the import boundary.

Stage 4 is complete implementation-wise. `cmd/filees-gui` is the composition root for `ipcclient`, the `app` model, the `tray` renderer, the `notifications` policy, the `actions` controller, and the platform adapter. The lifecycle shares cancellation across system signals, a full FileES restart/shutdown, and tray shutdown; it waits on controller tasks and renderer listeners, and a manual reconnect goes through the `app` event loop. A per-user lock blocks a second instance before tray initialization. A vertical test with a real IPC transport covers multiple repositories plus daemon shutdown and restart; a server shutdown closes active streams, so reconnect does not depend on process death. The `packaging/build-gui.sh` script produces a pure-Go Linux/Windows bundle in fresh directories and passes the version from `VERSION` into the GUI and WiX. Linux gets a per-user install, and the WiX MSI source creates a Windows Start Menu shortcut with the AUMID. Acceptance in real sessions on both systems is described in `packaging/ACCEPTANCE.md` and remains a release gate.

GUI-process autostart is managed without starting the tray, via `filees-gui --autostart status|enable|disable`. Status distinguishes a correct `enabled` entry from `enabled-stale`, which points at a different command. The entry keeps the executable's absolute path and the `--socket` parameter, so `enable` should only be run once the file is in its final install location. The operation is per-user: XDG on Linux and HKCU on Windows.

### First-release scope

The first vertical slice is considered ready when:

- the GUI starts and exits independently of the daemon,
- it correctly shows disconnection, reconnect, and a multi-repository snapshot,
- it reacts to events, and performs a full resync after a sequence gap,
- it shows only actions available in the capabilities,
- lock/unlock works only through `ipcclient` and presents structured errors,
- a slow or closed GUI never blocks the daemon,
- autostart works on Linux and Windows,
- an architectural test protects the ban on importing engine packages,
- presentation-model tests need no running SVN nor a graphical environment.

Out of MVP scope: configuration editing, daemon service management, `pause/resume`, manual sync/publish, interactive conflict decisions, a full application window, and macOS.

---

## Architecture

```text
filees-gui / filees CLI
          │ contract/v1 over a local Unix-domain socket
          ▼
daemon: projection + supervisor + provisioning + repository pipelines
          │                    │
          │ svn+ssh            └── control/v1 over SSH
          ▼                                      ▼
data repositories                     forced entry -> OpenBSD worker
                                                  │
                                      canonical records + service repo
                                                  │
                                      view.json comes back as a projection
```

The server has no resident "FileES daemon". The system `sshd` runs
restricted entrypoints and workers for individual operations.
Authorization, grants, editing policy and client views are owned by the
server; the GUI is presentation only, and the daemon remains the owner of
local and SVN state.

An independent pipeline runs for every repository:

```
Scanner (watcher) ──events──► Commit Service ──svn──► SVN server
```

### Scanner (`pkg/watcher`)

Periodically walks the working-copy tree and detects changes:

- Checks the file's mtime and size
- If the size is ≤ 64 MiB and the MD5 budget allows it, computes a hash and compares it with the previous one; identical content generates no event
- Larger files go into a backlog (`md5.backlog.json`); a **backlog worker** (a separate goroutine) hashes MD5 in the background every 5 s, one file at a time, always picking the smallest — the result flows back into `s.cur`, which enables detecting renames of large files
- Deletions are debounced for no longer than the new-file publication delay (currently 5 min)
- During a commit (`commit.busy`) it switches into a lightweight mode — watching only `.filees/tickets/`
- Symlinks are skipped (FS-0201); visible in logs at the `debug` level

**Operating modes:**

| Mode | Description |
|------|------|
| `Baselining` | First run — builds the manifest without emitting events |
| `Active` | Normal mode — scans and emits events to the commit service |

The transition from Baselining to Active is automatic after the first successful scan — no external flag is required.

### Commit Service (`pkg/commit`)

Collects events from the scanner into a staging map and commits every `commit_interval`:

1. Snapshots pending changes (respecting the minimum delay for new files — 5 min)
2. Plans a batch by the real file count and byte total; a single file larger than the limit gets its own batch
3. Once `backlog_flush_mib` is exceeded, forces publication without waiting for the normal interval
4. Filters through an explicit `svn status --verbose` (distinguishing `unversioned`, `added` and `normal`)
5. Runs a non-recursive `svn add --parents --depth empty`, so a directory does not bypass the limits, then delete/lock/commit
6. Records the revision number to `head.rev`
7. If the committed files match `shout_patterns`, creates a notification ticket

During `SIGINT`/`SIGTERM`, the service stops accepting new changes, drains the tail of watcher events, and empties all of staging in bounded batches. Draining has its own `shutdown_commit_timeout`; whatever isn't sent is atomically preserved in the commit cache and resumed on the next start. `SIGKILL`, an OOM kill and power loss never let shutdown code run, so the durable cache remains the mandatory recovery path. A restart skips paths the server already accepted and publishes only the remaining part of the cache.

At startup, and in parallel in the **HEAD poller** (every `poll_interval`, defaulting to 30s):
- the daemon runs `svn cleanup` and, as long as the working copy has no locally missing paths, `svn update`; a `missing` entry defers the update, so it does not recreate a local delete nor a rename source
- startup conflicts go through the same lossless reconciliation as poller conflicts
- `svn info --show-item revision <repo_url>` fetches the server's HEAD revision
- if HEAD > the local revision, runs `svn update`
- handles offline and backoff identically to a commit
- after an update, detects conflicts and runs **reconciliation**

**Reconciliation** (policy: HEAD wins):
1. Detects conflicted files in `svn update`'s output (`C ...` lines)
2. Copies the local version (`<file>.mine` or the file itself) into `!kolizje/<timestamp>_lokalne/`
3. Writes a `.meta` file with metadata (original path, size, timestamp)
4. Runs `svn resolve --accept theirs-full` — the server's version wins
5. Emits `RECON-3002` to `errors.jsonl`

The `!kolizje/` directory is automatically ignored by the scanner — it never enters a commit.

Event coalescing in staging:
- `Added + Modified → Added`
- `Deleted + Added → Added` (the file came back — treated as new)
- `Modified + Deleted → Deleted`

### IPC Server (`pkg/ipcserver`)

The daemon exposes a Unix socket and accepts connections from the CLI and the GUI. Protocol: JSON Lines, `filees.contract/v1` format.

- Each connection serves one request and closes (request/response), except for `events.subscribe`, which switches the connection into push mode (the server sends events until the client disconnects)
- `RepoState` — a live snapshot of repository state, updated by the daemon, served to clients with no engine access
- Every path in `repo.lock`/`repo.unlock` is validated — it must be absolute and lie inside a working copy (`LOCK-2002` on violation)
- `repo.reservation_list` aggregates only live locks visible from that
  server's locally attached WCs; `repo.reservation_release` re-reads the
  lock and requires its token, repo, server, and a safe relative path

Implemented commands are capability-gated and cover system lifecycle,
signed updates, activation/pairing, repository lifecycle (including
`repo.load_dump`), grants and realm visibility (`repo.grant_access`,
`repo.revoke_access`, `realm.grant_recipients`, `realm.set_visibility`),
activity, `repo.lock`, `repo.unlock`, `repo.reservation_list`,
`repo.reservation_release`, `error.list` and `events.subscribe`.
Once the durable actor is wired in, the daemon also advertises
`whale.list`, `whale.get`, `whale.put_begin`, `whale.get_begin`,
`whale.get_confirm`, `whale.retry` and `whale.cancel`. These are intents
neutral to any particular editor: the GUI, a CAD helper, and another
third-party plugin all observe the same saved state, and the
`whale.changed` event is merely a signal to refetch the projection.

`whale.get_begin` can take a full generation identity, or just the repo, a
logical path, and a snapshot revision. In the second variant, the server
performs a metadata-only `GET_DISCOVER`, prices out the size and SHA, but
does not reserve space nor create a cache entry. Bytes can move only after
`whale.get_confirm`.

### IPC Client (`pkg/ipcclient`)

The library used by the CLI and the GUI. Every plain call opens a new connection to the socket, sends the request, and waits for the response. `Subscribe` keeps a separate, long-lived event connection. The client validates responses (protocol, request_id, status), subscription ACKs, and required event fields. The connection deadline inherits from the caller's context — a lock/unlock with a 30 s context gets 30 s instead of the 10 s default; canceling the context also unblocks the handshake and waiting for the next event.

### SVN Client (`pkg/client`)

A wrapper around the `svn` CLI. All calls are serialized by an in-process mutex. Timeout per command: 30 minutes. Called exclusively by the daemon — the CLI never invokes SVN directly.

### Runtime Gates (`pkg/runtime`)

Optional mechanisms that limit commit concurrency:

- **HostGate** — a limit of K concurrent commits at the host scale (locked via `mkdir`)
- **RepoMutex** — at most 1 commit at a time per repository

The lock directory carries the PID and a unique owner token. After a crash, the next process atomically takes over a dead PID's lock; the token prevents a late `release` from deleting a newer lock. Old directories with no metadata are taken over only after a grace period. Both mechanisms return a release function that is safe across multiple goroutines.

### Error Classifier (`pkg/errmap`)

Classifies Go errors into structured entries ready for logging and UI handling.

```
Classify(err) → Entry{Code, Severity, Hint, Msg, Details}
```

| Code | Severity | Hint | Description |
|------|----------|------|------|
| `NET-4007` | WARN | RETRY_BACKOFF | No connection to the server |
| `AUTH-4102` | ERROR | ADMIN_ONLY | Authentication error |
| `LOCK-2001` | ERROR | REQUIRE_ACTION | File locked by another user |
| `COMMIT-3101` | WARN | RETRY_LOCAL | WC out of date — update required |
| `COMMIT-3102` | WARN | RETRY_LOCAL | Path outside version control |
| `COMMIT-3100` | ERROR | RETRY_LOCAL | Generic commit error |
| `SYNC-0000` | ERROR | RETRY_LOCAL | Unclassified error |

`Sink` writes entries as JSON Lines to `<wc>/.filees/logs/errors.jsonl` (one JSON object per line). Format:

```json
{"ts":"2026-07-13T10:00:00Z","scope":"commit:projectA","code":"NET-4007","severity":"WARN","hint":"RETRY_BACKOFF","msg":"Network unreachable — retrying with backoff","details":"..."}
```

### Tickets (`pkg/tickets`)

Creates notification files at `.filees/tickets/NOTICE-<uuid>.req` in INI format. Used to signal events to external tools (e.g. the tray UI).

File format:
```ini
TYPE=NOTICE
CLIENT=<uuid>
TS=<RFC3339>
ID=<uuid>

[payload]
TITLE=Pending commit: 5 paths
<body>
```

---

## State file layout

The daemon creates a `.filees/` directory inside the working copy:

```
<wc>/
├── !kolizje/
│   └── YYYY.MM.DD@HH.MM_lokalne/
│       ├── <relpath>           # local copy of the file before the conflict
│       └── <relpath>.meta      # JSON: orig_rel, timestamp, size, type
└── .filees/
    ├── state/
    │   ├── manifest.json       # active manifest (mtime, size, MD5)
    │   ├── manifest.tmp        # manifest being built in Baselining mode
    │   ├── commit.busy         # flag: a commit is in progress (TTL 10 min)
    │   ├── head.rev            # last committed/updated revision
    │   ├── client.uuid         # stable client UUID (generated once, durable)
    │   ├── daemon.pid          # daemon PID (removed on shutdown)
    │   └── md5.backlog.json    # queue of large files awaiting an MD5 hash
    ├── commit_cache/
    │   └── cache.json          # staging map (survives a daemon restart)
    ├── tickets/
    │   └── NOTICE-<uuid>.req   # outgoing notifications
    ├── logs/
    │   └── errors.jsonl        # structured error log (JSON Lines, append-only)
    └── locks/
        ├── global/             # HostGate slots
        └── repo/               # RepoMutex locks

$XDG_RUNTIME_DIR/filees.sock   # daemon IPC socket (or ~/.filees/daemon.sock)
$XDG_DATA_HOME/filees/whales/ # actor operations, PUT payload.ready and resume state
```

---

## File ignoring

The daemon always ignores: `.svn/`, `.filees/state/`, `.filees/locks/`.

### Built-in patterns (hardcoded, cannot be overridden)

| Category | Patterns |
|-----------|--------|
| Office temp files | `~$*` (MS Office), `.~lock.*#` (LibreOffice/OpenOffice), `*.tmp`, `*.bak` |
| OS metadata | `.DS_Store`, `Thumbs.db`, `desktop.ini` |
| Editor directories | `.vscode/`, `.idea/` |
| Editor files | `*.swp`, `*.swo` |
| Build artifacts | `node_modules/`, `__pycache__/`, `*.o`, `*.pyc` |
| Other VCS | `.git/` |

Directories (`.git/`, `node_modules/`, etc.) are skipped entirely — the watcher never descends into them.

### User patterns

The `.filees/user_ignore.cfg` file (hot-reloaded on every scan):

```
# comment
*.local
!archive/         # hard ignore — skips the whole subdirectory
assets/**/thumb   # ** matches any depth
```

Patterns starting with `!` are "hard" ignores — on a directory, they skip the entire subtree.

---

## Signals

| Signal | Action |
|--------|-----------|
| `SIGINT` / `SIGTERM` | Graceful shutdown — waits for every pipeline to finish |

---

## Dependencies

| Package | Version | Use |
|--------|--------|------|
| `github.com/google/uuid` | v1.6.0 | Generating ticket and IPC request IDs |

---

## Internal packages

| Package | Role |
|--------|------|
| `pkg/watcher` | Filesystem scanner + MD5 backlog |
| `pkg/commit` | Commit service, HEAD poller, reconciliation |
| `pkg/client` | SVN CLI wrapper |
| `pkg/config` | `config.json` parsing |
| `pkg/contract/v1` | IPC protocol types (`filees.contract/v1`) |
| `pkg/control/v1` | Versioned ticket/result control-plane envelopes (`filees.control/v1`) |
| `pkg/whale/v1` | Path/generation canon, states, and Whale transport framing |
| `pkg/whaleclient` | Durable PUT/GET actor, spool/partial, exact offsets, and pinned SSH |
| `pkg/provisioning` | Durable state machine for repo creation and the initial commit |
| `pkg/clientview` | Strict decoding of the installation projection from the service repo |
| `pkg/localrepo` | Durable lifecycle of local WC attachments and paths |
| `pkg/reposupervisor` | Dynamic start, stop, and reconciliation of repositories |
| `pkg/passport` | Edit passports, fencing, and `svn:needs-lock` migration |
| `pkg/repoworker` | Canonical repository records, grants, policies, and projections |
| `internal/whaleworker` | Server-side PUT/commit, metadata-only discovery/quote, seekable GET cache and its retention; sessions supervised by `internal/servertool` |
| `pkg/ipcserver` | Unix-socket server for the CLI/GUI |
| `pkg/ipcclient` | IPC client used by the CLI and the GUI |
| `contracttests` | Cross-cutting conformance gate for envelopes, capabilities, and IPC round-trips |
| `pkg/errmap` | Error classification + writing to `errors.jsonl` |
| `pkg/runtime` | HostGate, RepoMutex |
| `pkg/talk` | Leveled logger and the `FILEES_LOG` variable |
| `pkg/tickets` | Writing `.filees/tickets/` notification files |

---

## License

BSD 2-Clause — see [LICENSE](LICENSE).
