# Releasing

How to build a release and publish it to https://brain.ohnopixel.com, the
site behind the one-line installers. Everything is built from this repository;
the site's source is `site/`.

## 1. Prepare

- Update [CHANGELOG.md](../CHANGELOG.md) with the new version and date, and
  merge everything to `main`.
- Check out the merge commit with a clean working tree. The builder refuses to
  run with uncommitted changes.
- Pick a version above the last one published. From 0.5.4, the binaries this
  guide builds update from this site, so the version only has to be above
  `curl -fsS https://brain.ohnopixel.com/releases/latest.json`. Installs
  of 0.5.3 and earlier still check the cloud server
  (`curl -fsS https://brain.ohnopixel.com/v1/cli/latest`) and, with auto-update
  on by default, replace themselves with any higher version found there.
- Install the Go version CI uses (`go-version` in
  `.github/workflows/release.yml`). The builds are reproducible: the same
  commit built with the same Go version gives byte-identical archives, so
  anyone can rebuild a release and compare its SHA-256s.

## 2. Build

```sh
go run ./packaging/release -version 0.5.14
```

This builds Linux, macOS and Windows binaries for amd64 and arm64 with the
same flags as `.goreleaser.yaml`, plus `-update-url` (default
https://brain.ohnopixel.com): the binaries update from this site's
`releases/latest.json` and `releases/<version>/` instead of the cloud server.
It checks that the binary for this machine reports the new version, and
writes the complete docroot to `dist/site`:

| Path | What it is |
| --- | --- |
| `index.html`, `docs/`, `assets/`, `favicon.svg` | the site, copied from `site/` |
| `install.sh`, `install.ps1` | the one-line installers |
| `releases/latest.json` | the release manifest; installers read it to find the current version |
| `releases/<version>/agent-brain_<os>_<arch>.tar.gz` (`.zip` on Windows) | the archives |
| `releases/<version>/checksums.txt` | SHA-256 of every archive, in `sha256sum` format |
| `releases/<version>/manifest.json` | the same manifest as `latest.json` |

The manifest uses the `release.Manifest` format that the update server serves
at `/v1/cli/latest`.

Re-running the builder replaces `dist/site`. It refuses to clear an `-out`
folder that does not hold an earlier build.

The archives' timestamps come from the commit, so a release can only be
rebuilt byte for byte from the commit it was built from:

| Version | Built from | Go |
| --- | --- | --- |
| 0.4.0 | `cac025b` | 1.26.9 |
| 0.5.0 | git tag `v0.5.0` | 1.26.9 |
| 0.5.1 | 0c48102 (wecansync feat/link-offer-setting) | 1.26.9 |
| 0.5.2 | 7e11a1a (wecansync feat/memory-list-paging) | 1.26.9 |
| 0.5.3 | 38a36da (wecansync feat/memory-list-paging) | 1.26.9 |
| 0.5.4 | e95b32d (wecansync feat/update-url) | 1.26.9 |
| 0.5.5 | 65dbb8d (wecansync ci/windows) | 1.26.9 |
| 0.5.6 | 38d8686 (wecansync fix/session-checkout) | 1.26.9 |
| 0.5.7 | 1682ced (wecansync feat/free-local-memory) | 1.26.9 |
| 0.5.8 | 03b808c (wecansync feat/memory-upkeep) | 1.26.9 |
| 0.5.9 | 755442c (wecansync feat/memory-upkeep) | 1.26.10 |
| 0.5.10 | ffbd6b6 (wecansync feat/memory-import) | 1.26.10 |
| 0.5.11 | 5c6eab1 (wecansync feat/memory-v2-default) | 1.26.10 |
| 0.5.12 | 7d0dc5c (wecansync feat/memory-by-session) | 1.26.10 |
| 0.5.13 | c1d8ba7 (wecansync main; published only at agentbrain.sync.com.lb) | 1.26.11 |

## 3. Publish the site

The cloud server at https://brain.ohnopixel.com serves the site from the
folder named by `AGENT_BRAIN_SITE_DIR` (`/var/lib/agent-brain/site` in
production): `/install.sh`, `/install.ps1`, `/favicon.svg`, and everything
under `/releases/`, `/docs/` and `/assets/`. The dashboard keeps `/`, so the
site's `index.html` is copied but not served.

Copy the new version's folder first and everything else second, so
`latest.json` never names a version whose archives are not on the server yet.
Do not use `rsync --delete`: older versions must stay downloadable for installs
pinned with `AGENT_BRAIN_VERSION`.

```sh
SITE=brain:/var/lib/agent-brain/site
rsync -rt --chmod=D755,F644 dist/site/releases/0.5.14 "$SITE/releases/"
rsync -rt --chmod=D755,F644 --exclude 'releases/0.5.14' dist/site/ "$SITE/"
```

0.5.13 was published only at agentbrain.sync.com.lb (agentbrain.wecansync.com
redirects there), and its binaries update from that site. Those installs move
back to brain.ohnopixel.com only when a later release is published there too.

## 4. Publish to the update server

Installs of 0.5.3 and earlier, and builds made without `-update-url`, read the
release from the cloud server's `/v1/cli/latest` instead. To reach them, copy
`dist/site/releases/<version>/manifest.json` and the archives it lists into
the server's release directory (`$AGENT_BRAIN_RELEASE_DIR`,
`/var/lib/agent-brain/releases`), so `/v1/cli/latest` names the new version
and `/v1/cli/download/<filename>` serves its archives. Those installs then
move to the site's update channel with that update.

## 5. Verify

```sh
curl -fsS https://brain.ohnopixel.com/releases/latest.json
cd "$(mktemp -d)" && curl -fsSLO https://brain.ohnopixel.com/releases/0.5.14/checksums.txt \
  && curl -fsSLO https://brain.ohnopixel.com/releases/0.5.14/agent-brain_linux_amd64.tar.gz \
  && sha256sum -c --ignore-missing checksums.txt
curl -fsSL https://brain.ohnopixel.com/install.sh \
  | AGENT_BRAIN_BIN_DIR="$(mktemp -d)" AGENT_BRAIN_NO_INTEGRATE=1 sh
```

The last command installs into a temporary folder and leaves your assistants
untouched.

## GitHub releases

Pushing a `vX.Y.Z` tag also runs the GoReleaser workflow
(`.github/workflows/release.yml`), which publishes archives with the same names
and contents on GitHub Releases. They are built separately, so their bytes and
`checksums.txt` differ from the site's; each set verifies against its own
checksums.

## Memory V2 operator activation

Publishing 0.5.0 does not upgrade a user database. Users must back up, align all readers and explicitly activate schema 16; see [Memory V2](memory-v2.md). A v0.5.0 GitHub tag identifies the reviewed public source. Keep that tag immutable after merging; reproducible rebuilds use its commit, even when main has a later merge commit.
