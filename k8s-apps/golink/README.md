# golink

Private shortlink service for the tailnet ([tailscale/golink](https://github.com/tailscale/golink)),
served at `https://go.snow-delta.ts.net/`.

Exposure is layer 7 ingress, `ingressClassName: tailscale`: the operator
publishes the hostname on the tailnet, provisions the certificate and injects
`Tailscale-User-*` headers. Golink itself runs in `-dev-listen` mode behind a
plain ClusterIP Service and never talks to the control plane — no `TS_AUTHKEY`,
no cert-manager, no public DNS.

- Links: SQLite at `/home/nonroot/golink.db` on PVC `golink`. Deliberately not
  backed up: the file holds shortlinks and click counts, nothing worth a
  `sqlite-dump` CronJob or a velero namespace. Losing the PVC loses the links.
- Access control is the tailnet: anyone who can reach the host can also create
  and delete links. Dev mode reports every request as `foo@example.com` and
  ignores the proxy's identity headers, so owners and click stats are not
  per-user; per-user identity needs tsnet mode with an auth key.
- `go/.all` lists every link, `go/.help` documents advanced links
  (`go/<short>/<path>` appends a path to the destination), `go/.metrics`
  exposes Prometheus metrics.
- An unknown short name answers 404 with a "create this link" form, so a link
  doubles as its own creation flow.

The hostname is the full `go.snow-delta.ts.net` on purpose: a tailnet
certificate covers only that name, and `https://go/` is a certificate mismatch.
That is why the short form lives in the browser (below) rather than in DNS.

## Firefox

Add a bookmark keyword pointing at the FQDN:

- Bookmarks → Manage bookmarks → New bookmark
- Name: `golink`
- Location: `https://go.snow-delta.ts.net/%s`
- Keyword: `go`

Then `go handbook` opens `https://go.snow-delta.ts.net/handbook`. `%s` takes
everything typed after the keyword, so `go pr/1234` opens `.../pr/1234`, and
Firefox gives bookmark keywords precedence over search engine aliases. Set
`browser.urlbar.suggest.bookmark` = `true` to preview the target URL in the
dropdown before pressing Enter.

No other pref is needed: nothing is fetched from a bare host, so neither
`browser.fixup.domainwhitelist.go` nor an HTTPS-Only Mode exception applies
here. The slash form `go/handbook` does not work — Firefox would treat it as
the bare host `go`, which answers on 443 with a certificate that does not cover
that name.

Firefox for Android has no keyword bookmarks, so links there are typed out as
full `https://go.snow-delta.ts.net/<short>` URLs.
