# docs

Static documentation site at [docs.terence.cloud](https://docs.terence.cloud),
served directly by [Garage](https://garagehq.deuxfleurs.fr) (bucket
`docs.terence.cloud`, exposed via the web ingress in `k8s-apps/garage/`). A
merge to main that touches this directory runs the `docs deploy` workflow:
`hugo deploy` syncs the rendered site to the bucket. No image is built.

## Layout

- `hugo.yaml` — site config, navigation, theme module import, deployment target
- `content/` — the pages, one markdown file per page

## Editing

```sh
nix develop # hugo is in the dev shell
cd workbench/docs
hugo server -D
```

Live reload at http://localhost:1313. Commit the regenerated `go.mod`/`go.sum`
if you change the theme version.

Publish with `hugo deploy` (needs `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`
for the garage S3 API, or just merge to main and let CI do it).

Theme: [hextra](https://github.com/imfing/hextra), pulled as a Hugo module
(never vendored). Callouts, cards, and tabs use hextra shortcodes; see the
theme docs for the full list.
