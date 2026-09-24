# Go TaaS Website

Static marketing/introduction page for the Go TaaS project, deployed to
[GitHub Pages](https://pages.github.com/).

## Structure

```
site/
├── index.html   # page structure (English is the default inline content)
├── styles.css   # styling, brand palette from assets/brand/README.md
├── i18n.js      # en / zh-CN translation dictionary
├── main.js      # language detection, switching, persistence
└── assets/      # brand assets (copied/symlinked from assets/brand)
```

## Local Preview

The site is fully static — any static file server works:

```bash
cd site
python3 -m http.server 8080
# open http://localhost:8080
```

## Language Switching

- The toggle button in the header switches between English and 简体中文.
- Preference order: `#lang=zh-CN` URL hash → `localStorage` → browser
  language → English (default).
- The chosen language is persisted in `localStorage` under the
  `go-taas-site-lang` key.

## Deployment

`.github/workflows/site.yaml` deploys the `site/` directory to GitHub Pages
on every push to `main` that touches `site/**` (or via manual dispatch).
Enable Pages in the repository settings with **Source: GitHub Actions**.
