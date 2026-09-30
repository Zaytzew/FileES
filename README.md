<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="branded-assets/filees-space-svg-pack/filees-space-monochrome-white.svg">
    <img src="branded-assets/filees-space-svg-pack/filees-space-monochrome.svg" alt="FileES" width="360">
  </picture>
</p>

# FileES

*English | [Polski](README.pl.md)*

FileES stores projects on your own server, keeps them in sync across
computers, preserves their history and helps people work on the same files.
Ordinary folders, your own programs, your rules. Underneath it is Apache
Subversion, used as transport and storage; users never need to know that.

The server side targets OpenBSD. Desktop apps run on Windows and Linux, and an
Android companion sends photos and files from the phone.

> ## Early access
>
> FileES is in early access. Signed releases are published and used in real
> work, but there are **no guarantees** — neither of correctness, nor of
> security, nor of data integrity — and no support.
>
> - **Two release channels.** *Beta* is an accepted release without features
>   still in preparation; the Microsoft Store version follows it. *Alpha* gets
>   new features and fixes first.
> - On-disk formats, protocols and commands may still change between releases.
> - This repository is a **filtered mirror** of the SVN repository: code, the
>   manual and the readme files. Internal design documents, plans and audit
>   reports are not published.
> - Issues and pull requests are not expected and may go unanswered.

---

## What it does

- **Ordinary folders on your disk**, kept in sync with a repository on your
  server by a background service. You keep using your own programs.
- **History of every file**: every published change can be looked at and
  brought back, including files deleted long ago.
- **Working together on binary files** (drawings, models, documents): a file
  can be *borrowed* for editing, so two people do not overwrite each other;
  others see it as read-only until it is published and returned.
- **Conflicts are decided by you**, with the local copy preserved, never
  silently overwritten.
- **Sharing**: repositories shared between people and computers, public
  download links (optionally with a password or an e-mailed code) and upload
  links for people without an account.
- **Android companion**: watched folders on the phone are sent to a chosen
  repository; repositories can be browsed and files downloaded.
- **Signed updates** for the desktop apps, the server and the Android app, from
  signed release channels.

## Getting FileES

Everything is published on **[filees.space/download](https://filees.space/download/)**
(beta) and **[filees.space/download-alpha](https://filees.space/download-alpha/)**
(alpha), each release with its SHA-256 and a short signed "what's new" list.

| What | Where |
|---|---|
| Windows | [Microsoft Store](https://apps.microsoft.com/detail/9P0J1BRL5K77) (beta, updated by the Store) or the MSI installer from the download page. Install one of them: each offers to remove the other. |
| Linux | AppImage from the download page. The first run installs FileES in your home directory; later versions arrive through the signed update channel. |
| Android | APK from the download page; the app checks the signed channel and can update itself. |
| Server | OpenBSD/amd64 bundle from the download page; installation in the [manual](https://manual.filees.space). Later upgrades come from the signed channel through `filees-install`. |

A desktop app needs a FileES server and an invitation from its administrator;
installing it does not give you storage. To look around first, use the **Demo**
button of a freshly installed app: it opens a time-limited account on a
demonstration server with a code sent to your e-mail address.

## Requirements

- **Windows:** Windows 10 version 2004 or later, or Windows 11, with the
  WebView2 Runtime (built into Windows 11). No OpenSSH, Subversion or VBScript
  is needed: the SSH and Subversion clients are built in.
- **Linux:** x86-64 with GTK 4 (4.10 or later) and WebKitGTK 6.0, a desktop
  with tray (SNI) support — on GNOME the AppIndicator extension — and a user
  systemd session. The SSH and Subversion clients are built in.
- **Server:** OpenBSD 7.9/amd64 as the primary and security-reference
  platform, the system `sshd` (FileES adds no network listener of its own),
  an independently installed `svnserve` reached as tunnel-mode `svnserve -t`
  over SSH, and an SMTP relay for e-mailed codes. A Linux/amd64 server bundle
  can be built, without OpenBSD's `pledge`/`unveil` confinement.

## Documentation

- **[manual.filees.space](https://manual.filees.space)** — the full manual in
  Polish and English: using the desktop and Android apps, shares, server
  installation, administration, operations, security and architecture.
- **[Manual pages](https://manual.filees.space/assets/man/index.html)** — the
  server tools' OpenBSD manual pages as HTML. The same pages are installed
  with the server (`man filees`) and upgraded with it; their sources are in
  [docs/man/](docs/man/).
- [manual/](manual/index.html) — the manual as mirrored in this repository;
  the site is the authoritative version.
- [USERGUIDE.md](USERGUIDE.md) — where to start in the manual.

## Building from source

Pre-built, signed releases are the supported way to install FileES. To build
from source you need the Go version named in [go.mod](go.mod); the tests also
need the Subversion command-line tools (`svn`, `svnadmin`).

```bash
make verify          # Go tests, selected race tests, go vet and an SVN recovery smoke test
go build ./cmd/filees                 # desktop daemon and CLI
make pair            # desktop daemon and app window, stamped with VERSION and the SVN revision
```

Release builds (Windows MSI and bundle, Linux AppImage, the OpenBSD server
bundle, the native Subversion and Explorer helpers) are described in
[tools/HOWTO-BUILD-CLIENT-RELEASE.md](tools/HOWTO-BUILD-CLIENT-RELEASE.md),
[tools/HOWTO-BUILD-SERVER-BUNDLE.md](tools/HOWTO-BUILD-SERVER-BUNDLE.md) and,
for signing and channels, [tools/RELEASE_PUBLISHING.md](tools/RELEASE_PUBLISHING.md).

### Where things are

| Path | Contents |
|---|---|
| `cmd/filees` | desktop daemon and command line |
| `cmd/filees-gui-wails` | desktop app window and tray (Wails, WebView) |
| `cmd/filees-*`, `internal/` | server tools, installer and updater, release tooling |
| `pkg/` | shared engine: sync, commit, IPC contract, SVN and SSH clients, server workers |
| `public-shares/` | public download and upload links (`filees-links`) |
| `android/` | Android companion (Kotlin, with the Go mobile client) |
| `native/` | native helpers: Subversion runtime, Windows Explorer anchor |
| `packaging/` | installers and packaging for Windows, Linux, OpenBSD and the website |
| `contracttests/` | conformance tests across the client/server contract |
| `docs/man/`, `manual/` | manual pages and the HTML manual |

The desktop is two processes: a background service that owns synchronization,
and an app window that talks to it only over a local, versioned IPC contract.
The architecture chapter of the manual describes both halves and the server.

## License

BSD 2-Clause — see [LICENSE](LICENSE).
