# golink

Private shortlink service for the tailnet ([tailscale/golink](https://github.com/tailscale/golink)),
served at `http://go/` from any device on the tailnet.

Exposure is a plain ClusterIP Service annotated `tailscale.com/expose` +
`tailscale.com/hostname: go`, so the tailscale-operator publishes it on the
tailnet as node `go` — the same pattern as `abs-dav`. No ingress, no
cert-manager, no public DNS, and no `TS_AUTHKEY`: golink runs in `-dev-listen`
mode and never talks to the control plane.

- Links: SQLite at `/home/nonroot/golink.db` on PVC `golink`. Deliberately not
  backed up: the file holds shortlinks and click counts, nothing worth a
  `sqlite-dump` CronJob or a velero namespace. Losing the PVC loses the links.
- Access control is the tailnet: anyone who can reach `go` can also create and
  delete links. Dev mode reports every request as `foo@example.com`, so owners
  and click stats are not per-user; per-user identity needs tsnet mode with an
  auth key.
- `go/.all` lists every link, `go/.help` documents advanced links
  (`go/<short>/<path>` appends a path to the destination), `go/.metrics`
  exposes Prometheus metrics.
- An unknown short name answers 404 with a "create this link" form, so a link
  doubles as its own creation flow.

Layer 3 exposure is what upstream documents for non-HTTP workloads, while HTTP
apps are steered to a layer 7 `Ingress` with an automatic certificate. That
route cannot serve this app: a tailnet certificate covers only
`go.snow-delta.ts.net`, so `go/handbook` would die on a name mismatch. L3
answers on port 80, where the bare MagicDNS name works. The annotation
spelling is also deliberate — MetalLB is installed, and this keeps the Service
off the LoadBalancer path.

## Firefox

Two settings, both required before `go/handbook` resolves:

- `browser.fixup.domainwhitelist.go` = `true` (boolean, in `about:config`).
  Without it Firefox sees a dotless host, decides it is not a URL, and hands
  the text to the default search engine.
- HTTPS-Only Mode needs `go` in its exceptions (Settings → Privacy & Security →
  HTTPS-Only Mode → Manage Exceptions → `http://go`). The operator's proxy
  listens on port 80 only — `https://abs-dav/` refuses connections the same
  way — so an upgraded request fails instead of resolving.

Then `go/handbook` in the address bar resolves in place, no extra keystrokes.

### Keyword alternative

Prefer `go␣handbook` over `go/handbook`? Add a bookmark instead of the pref;
bookmark keywords take precedence over search engine aliases:

- Bookmarks → Manage bookmarks → New bookmark
- Location: `http://go/%s`
- Keyword: `go`

`%s` takes everything typed after the keyword, so `go pr/1234` opens
`http://go/pr/1234`. Set `browser.urlbar.suggest.bookmark` = `true` to preview
the target URL before pressing Enter.

Firefox for Android has neither `about:config` pref editing nor keyword
bookmarks, so links there are typed out as full `http://go/<short>` URLs.
