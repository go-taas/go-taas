# Vendored browser libraries

These files are third-party builds vendored so the site stays a **self-contained
static artifact**: it needs no bundler, no package manager, and no runtime CDN
dependency, and it works offline and on GitHub Pages unchanged.

| Library | Version | License | Vendored path | Loaded |
| --- | --- | --- | --- | --- |
| [marked](https://github.com/markedjs/marked) | 18.1.1 | MIT | `marked/marked.umd.js` | always (documentation pages) |
| [highlight.js](https://github.com/highlightjs/highlight.js) | 11.12.0 | BSD-3-Clause | `highlight/highlight.min.js` | always (documentation pages) |
| [mermaid](https://github.com/mermaid-js/mermaid) | 12.2.0 | MIT | `mermaid/mermaid.esm.min.mjs` + `mermaid/chunks/` | lazily, only when a document contains a Mermaid diagram |

`versions.json` records what is actually vendored; `LICENSE` files sit next to
the builds they cover.

## Why these are committed

The documentation browser renders Markdown in the browser. Without a vendored
renderer the site would have to call a third-party CDN at read time, which would
add a supply-chain dependency and break offline/air-gapped previews. Mermaid is
vendored as its ES-module build: the ~31 KiB entry point pulls only the diagram
bundles a page actually uses, so a document with a flowchart downloads roughly
2.4 MiB instead of the 5.5 MiB of the single-file UMD build.

## Regenerating

```bash
node site/scripts/vendor.mjs           # install the pinned versions and re-copy
node site/scripts/vendor.mjs --check   # fail if site/vendor/ drifts from the pins
```

Bump the pins in `site/scripts/vendor.mjs` and in the table above together, then
commit the refreshed tree in the same change.
