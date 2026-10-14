# Releasing

How to build a release and publish it to https://agentbrain.wecansync.com, the
site behind the one-line installers. Everything is built from this repository;
the site's source is `site/`.

## 1. Prepare

- Update [CHANGELOG.md](../CHANGELOG.md) with the new version and date, and
  merge everything to `main`.
- Check out the merge commit with a clean working tree. The builder refuses to
  run with uncommitted changes.
- Pick a version above the one the update server offers
  (`curl -fsS https://brain.ohnopixel.com/v1/cli/latest`). Installs check that
  server and, with auto-update on by default, replace themselves with any
  higher version, so a lower number would be undone at the next check.
- Install the Go version CI uses (`go-version` in
  `.github/workflows/release.yml`). The builds are reproducible: the same
  commit built with the same Go version gives byte-identical archives, so
  anyone can rebuild a release and compare its SHA-256s.

## 2. Build

```sh
go run ./packaging/release -version 0.5.0
```

This builds Linux, macOS and Windows binaries for amd64 and arm64 with the
same flags as `.goreleaser.yaml`, checks that the binary for this machine
reports the new version, and writes the complete docroot to `dist/site`:

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

## 3. Publish the site

The site is served by Apache from `/var/www/agentbrain.wecansync.com`. Build
on the web server from a clone of this repository, then copy the new
version's folder first and everything else second, so `latest.json` never
names a version whose archives are not on the server yet. Do not use
`rsync --delete`: older versions must stay downloadable for installs pinned
with `AGENT_BRAIN_VERSION`.

```sh
DOCROOT=/var/www/agentbrain.wecansync.com
mkdir -p "$DOCROOT/releases"
rsync -rt --chmod=D755,F644 dist/site/releases/0.5.0 "$DOCROOT/releases/"
rsync -rt --chmod=D755,F644 --exclude 'releases/0.5.0' dist/site/ "$DOCROOT/"
```

The Apache host is
[packaging/apache/wecansync-agentbrain.conf](../packaging/apache/wecansync-agentbrain.conf);
its header comment has the commands that enable it and add HTTPS. The
server's certbot is a pip install without a renewal timer, so the certificate
renews from its own cron entry, `/etc/cron.d/certbot-agentbrain`:

```sh
47 3 * * * root /usr/bin/certbot renew --cert-name agentbrain.wecansync.com --quiet
```

On nginx instead, an equivalent server block:

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name agentbrain.wecansync.com;
    # ssl_certificate / ssl_certificate_key: for example from certbot

    root /var/www/agentbrain.wecansync.com;
    index index.html;

    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;
    add_header Content-Security-Policy "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" always;

    # Installers and the manifest change with every release: never cache them.
    location = /install.sh  { default_type text/plain; add_header Cache-Control "no-cache" always; }
    location = /install.ps1 { default_type text/plain; add_header Cache-Control "no-cache" always; }
    location = /releases/latest.json { add_header Cache-Control "no-cache" always; }

    # A published version never changes.
    location /releases/ { add_header Cache-Control "public, max-age=31536000, immutable" always; }

    location / { try_files $uri $uri/ =404; }
}

server {
    listen 80;
    server_name agentbrain.wecansync.com;
    return 301 https://$host$request_uri;
}
```

## 4. Publish to the update server

`agent-brain update` and the background self-updater read the release from
the backend, not from the site. Copy `dist/site/releases/<version>/manifest.json`
and the archives it lists into the backend's release directory
(`$AGENT_BRAIN_RELEASE_DIR`), so `/v1/cli/latest` names the new version and
`/v1/cli/download/<filename>` serves its archives.

## 5. Verify

```sh
curl -fsS https://agentbrain.wecansync.com/releases/latest.json
cd "$(mktemp -d)" && curl -fsSLO https://agentbrain.wecansync.com/releases/0.5.0/checksums.txt \
  && curl -fsSLO https://agentbrain.wecansync.com/releases/0.5.0/agent-brain_linux_amd64.tar.gz \
  && sha256sum -c --ignore-missing checksums.txt
curl -fsSL https://agentbrain.wecansync.com/install.sh \
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
