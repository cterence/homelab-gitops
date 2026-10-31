# docs

Static documentation site at [docs.terence.cloud](https://docs.terence.cloud),
built by the standard workbench factory: a merge to main that touches this
directory bumps the image tag, Kaniko rebuilds, and ArgoCD rolls the
deployment. Do not bump `build.yaml` manually.

## Layout

- `hugo.yaml` — site config, navigation, theme module import
- `content/` — the pages, one markdown file per page
- `Dockerfile` — Hugo (extended, compiled from source) renders `content/`,
  the result is served by httpd

## Editing

```sh
nix develop # hugo is in the dev shell
cd workbench/docs
hugo server -D
```

Live reload at http://localhost:1313. Commit the regenerated `go.mod`/`go.sum`
if you change the theme version.

Theme: [hextra](https://github.com/imfing/hextra), pulled as a Hugo module
(never vendored). Callouts, cards, and tabs use hextra shortcodes; see the
theme docs for the full list.
