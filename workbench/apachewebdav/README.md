# apachewebdav

Apache httpd image with WebDAV enabled, configured entirely through
environment variables at container startup.

Migrated from https://github.com/cterence/docker-apachewebdav (published
on GHCR as `ghcr.io/cterence/apachewebdav`).

## How it works

The Dockerfile layers a WebDAV setup on top of the stock `httpd` image:
it enables the `mod_dav`/`mod_dav_fs` and auth modules, includes
`conf/conf-enabled/*.conf` and `conf/sites-enabled/*.conf`, and wires in
the `dav.conf` snippet plus a default vhost.

At startup, `docker-entrypoint.sh` rewrites the Apache config from
environment variables and then `exec`s httpd:

- `SERVER_NAMES` — comma-separated vhost names (first becomes ServerName)
- `LOCATION` — path prefix served by the DAV alias (default `/`)
- `AUTH_TYPE` — `Basic` (default) or `Digest`
- `REALM` — auth realm name
- `USERNAME` / `PASSWORD` — credentials written to `/user.passwd`
- `ANONYMOUS_METHODS` — methods allowed without auth, or `ALL`
- `SSL_CERT` — `selfsigned` generates a cert for `/cert.pem`+`/privkey.pem`
- `PUID` / `PGID` / `PUMASK` — uid/gid/umask httpd runs as (default 1000)
- `NO_CHOWN_DATA` — skip chowning `/var/lib/dav/data`
- `READONLY` — read-only DAV

WebDAV content lives in `/var/lib/dav/data`; the lock database is
`/var/lib/dav/DavLock`.

## Deployment

Consumed by `k8s-apps/audiobookshelf` and `k8s-apps/calibre-web` (webdav
sidecar containers), image `registry.terence.cloud/apachewebdav:<tag>`
matching `build.yaml`. Bump both tags together on changes. When the httpd
base version changes, update the FROM image and bump the tags.
