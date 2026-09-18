# filees.space: automatic download page

`https://filees.space/download/` offers the installers of whatever release the
signed channel `alpha.v2` currently promotes — one per platform named in
`download.json`, today the Windows MSI and the Linux AppImage, always from the
same release. Nobody uploads installer files any more: signing and promoting a
release is the publication, and the site follows within 15 minutes.

## How it works

`cmd/filees-site-download` runs from cron on the web server as the system user
`filees-site`. Each run:

1. reads `channels/alpha.v2.json` and its signature from
   `svn://cloud.atmprojekt.pl/FILEES-BIN` (anonymous read) and verifies both
   the channel and the release manifest with the release key in
   `release-key.pub` — the same resolver the desktop client's self-update uses,
   including the manifest identity binding and channel expiry;
2. refuses a channel that points at an older release than the one it last
   published (sequence and security epoch, remembered in
   `/var/lib/filees-site/state.json`);
3. resolves every platform of that one release, fetches an installer only when
   the page is not already current, and checks its size and SHA-256 against the
   signed manifest of its platform;
4. writes the installers, a `SHA256SUMS` line for each and the page from
   `download.html` into a new directory and swaps it in with renames.

A platform missing from the release stops the run, so the page never offers one
platform from a new release and another from an old one.

Any error leaves the previous page untouched and is logged. The trust anchor is
`landing/release-key.pub` in the site sources, never a key read from the
repository being verified. The landing build (`node landing/build.mjs`) runs the
same program, so a local preview shows exactly what cron would publish.

Layout on the server:

| Path | Owner | What |
|---|---|---|
| `/usr/local/bin/filees-site-download` | root | the program |
| `/usr/local/share/filees-site/` | root | `download.json`, `release-key.pub`, `download.html` |
| `/var/lib/filees-site/site/download/` | filees-site | the published page and installers |
| `/var/lib/filees-site/state.json` | filees-site | last published release |
| `/var/www/filees.space/download` | root | symlink to the published directory |
| `/etc/cron.d/filees-site-download` | root | every 15 minutes, `flock`-guarded |

The service user can write only its own directory, never the web root.

## Install or update (once, and after changing the program or its files)

On the build machine, from the source working copy:

```sh
node packaging/site/stage.mjs
scp -r dist/site-publisher ADMIN@WEB-SERVER:~/
```

On the server:

```sh
sudo sh ~/site-publisher/install.sh
```

The script creates the user, installs the files, publishes once (a release that
does not verify stops it before the web root is touched), moves a manually
uploaded `/var/www/filees.space/download` aside to
`/var/lib/filees-site/download.manual-<date>`, links the web root to the
publication and installs the cron entry. Running it again is safe.

Check: `https://filees.space/download/` and `journalctl -t filees-site-download`.
Nginx follows symlinks unless `disable_symlinks` is set for the site.

## Everyday use

- **New release:** sign and promote it to `alpha` as usual. Nothing else.
- **Release notes:** add the release ID to `notes` in `landing/download.json`,
  commit, then copy the file to the server:
  `sudo install -m 0644 download.json /usr/local/share/filees-site/`. A release
  without notes gets a page without the highlighted box.
- **Page text:** edit `landing/download/index.html`, commit, stage and install
  again.
- **New platform:** add it to `platforms` in `landing/download.json`, give the
  page its `{{<PLATFORM>_FILE}}`, `{{<PLATFORM>_SHA256}}` and
  `{{<PLATFORM>_SIZE_PL|EN}}` placeholders (the platform upper-cased, `-` as
  `_`), teach `installerNamePatterns` in `publish.go` what its installer is
  called, then stage and install again.

## Remove

```sh
sudo rm /etc/cron.d/filees-site-download /var/www/filees.space/download
```

Then put a static `download/` back in the web root if the page should stay.
