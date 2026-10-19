# Go TaaS Website

Static marketing page and documentation browser for the Go TaaS project,
deployed to [GitHub Pages](https://pages.github.com/).

## Structure

```
site/
├── index.html       # landing page (English is the default inline content)
├── docs.html        # documentation browser
├── styles.css       # landing page styling, brand palette from assets/brand/README.md
├── docs.css         # documentation browser styling (reuses the tokens above)
├── i18n.js          # en / zh-CN translation dictionary
├── main.js          # language detection, switching, persistence
├── docs.js          # documentation browser (routing, search, Markdown/Mermaid rendering)
├── scripts/
│   ├── build-docs-manifest.mjs   # indexes site/docs/** into site/docs/manifest.json
│   └── vendor.mjs                # refreshes the vendored browser libraries
├── vendor/          # pinned third-party builds (see vendor/README.md)
├── docs/            # the documentation set, served as static files
│   ├── manifest.json  # generated index (do not edit by hand)
│   ├── design/        # product/UI design documents, EN + zh-cn per feature point
│   ├── architecture/  # architecture and detailed design documents, EN + zh-cn
│   └── api/           # generated OpenAPI specs (git-ignored; see below)
└── assets/          # brand assets (copied/symlinked from assets/brand)
```

## Local Preview

The site is fully static — any static file server works:

```bash
cd site
python3 -m http.server 8080
# landing page:  http://localhost:8080
# documentation: http://localhost:8080/docs.html
```

Open the pages through the server rather than `file://`: the documentation
browser fetches `docs/manifest.json` and the document bodies, which browsers
block on the `file:` scheme.

## Documentation Browser

`docs.html` renders the whole `site/docs/` set in the browser:

- **Navigation & search** — every document is indexed by title, feature point,
  scope and heading text; `/` focuses the search box, and the sidebar filters as
  you type.
- **Reading** — rendered Markdown, syntax-highlighted code with copy buttons,
  Mermaid diagrams that start fitted to the reading column (so a wide diagram
  never dominates the article) and can be zoomed and panned freely — toolbar
  buttons, Ctrl/⌘ + wheel, pinch, drag, `+` / `−` / `0`, double-click, and a
  fullscreen view — plus the document's own metadata card, an in-page table of
  contents with scroll tracking and a reading-progress bar.
- **Language** — the header toggle switches the whole UI; each document also has
  a button that pins *that document* to the other language, and cross-references
  between the two sets navigate to the matching variant.
- **Deep links** — the view lives in the URL:
  `docs.html?doc=<section>/<feature>#doc=…&lang=en&dl=zh-CN&h=<heading-anchor>`.
- **API reference** — when `make pbgen` has generated the OpenAPI specs into
  `site/docs/api/`, an API section lists every service's operations. The specs
  are generated artifacts and are **not** committed, so that section appears only
  in builds where they exist.

Rendering uses the vendored libraries in `site/vendor/` (marked, highlight.js and
Mermaid; Mermaid loads only for documents that contain a diagram), so the site
has no runtime CDN dependency and works offline. See
[`vendor/README.md`](./vendor/README.md) for the pinned versions and how to
refresh them.

### Diagram sizing and zoom

Mermaid renders at its natural size, which for the architecture diagrams is
several thousand pixels wide. Each diagram is therefore shown inside a bounded
viewport that:

- **starts fitted to the reading column** (never enlarged past 100%), so it
  occupies a sensible amount of the page and the article stays readable;
- **zooms freely** from 5% to 800% — toolbar `−` / percentage / `+`, **Fit width**
  and **Actual size**, Ctrl/⌘ + wheel around the pointer, trackpad pinch, and
  `+` / `−` / `0` when the diagram has focus;
- **pans** by dragging, with the wheel scrolling the page as usual unless Ctrl/⌘
  is held;
- **re-fits on resize** only while the reader has not zoomed manually;
- opens in a **fullscreen** overlay that fits the viewport and carries the same
  controls (`Esc`, the close button, or a click on the backdrop closes it).

If Mermaid cannot be loaded, the diagram falls back to its highlighted source.

### Regenerating the document index

`site/docs/manifest.json` is generated from `site/docs/**` and committed so a
plain static server can serve the browser without running Node:

```bash
node site/scripts/build-docs-manifest.mjs           # rewrite the manifest
node site/scripts/build-docs-manifest.mjs --check   # fail if it is stale
```

Re-run it whenever a document is added, renamed, retitled or its headings change.

## Language Switching

- The toggle button in the header switches between English and 简体中文.
- Preference order: `#lang=zh-CN` URL hash → `localStorage` → browser
  language → English (default).
- The chosen language is persisted in `localStorage` under the
  `go-taas-site-lang` key.
- Markup can opt into translation with `data-i18n` (text), `data-i18n-html`
  (markup) or `data-i18n-placeholder` (input placeholder); `window.SITE_LANG`
  and the `go-taas:lang` event let a page react to a language change.

## Deployment

`.github/workflows/site.yaml` deploys the `site/` directory to GitHub Pages on
every push to `main` that touches `site/**` (or via manual dispatch). The
workflow regenerates the documentation manifest and verifies the vendored
libraries before uploading.

Enable Pages in the repository settings with **Source: GitHub Actions**.
