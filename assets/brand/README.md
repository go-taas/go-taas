# Go TaaS Logo

![Go TaaS brand preview](./preview.png)

## Design Concept

**Token Stream: turn tokens into a service.** The mark is built around the T of TaaS; the separate slice at the top represents a token that can be independently consumed and metered, while the angled cut expresses streaming output and execution efficiency. The bold strokes, flat two-color palette, and open silhouette suit console navigation and small avatars, with no reliance on gradients, shadows, or thin lines.

The product name, wordmark, and preview labels are uniformly **Go TaaS**, with G, T, and S capitalized. Technical identifiers such as project paths, file names, and CSS class names keep the `go-taas` form. This mark does not use the official Go or Kubernetes logos and does not imply official endorsement.

## Asset Selection

| File | Purpose |
| --- | --- |
| [avatar-512.png](./avatar-512.png) | GitHub organization or personal avatar, 512 × 512, opaque light background |
| [avatar-1024.png](./avatar-1024.png) | High-resolution avatar and display, 1024 × 1024 |
| [avatar.svg](./avatar.svg) | Avatar vector source, with padding reserved for a circular crop |
| [logo.svg](./logo.svg) | Transparent standalone icon for light UI |
| [logo-dark.svg](./logo-dark.svg) | Transparent standalone icon for dark UI |
| [logo-mono.svg](./logo-mono.svg) | Monochrome print, CSS mask, or inline SVG |
| [wordmark.svg](./wordmark.svg) | Horizontal wordmark for light UI, text converted to paths |
| [wordmark-dark.svg](./wordmark-dark.svg) | Horizontal wordmark for dark UI, text converted to paths |
| [preview.svg](./preview.svg) / [preview.png](./preview.png) | Brand preview; not intended as a product icon |

## Colors and Sizes

| Role | Light background | Dark background |
| --- | --- | --- |
| Primary mark | `#007F86` | `#42CDD0` |
| Token slice | `#F06445` | `#FF896B` |
| Wordmark | `#17272B` | `#F3F7F7` |

- Avatar background: `#F3F7F7`; recommended dark background: `#17272B`.
- Standalone icon: 24–48 CSS px recommended, 16 px minimum; do not pair with text at 16 px.
- Horizontal wordmark: 138–184 CSS px wide recommended, keeping the `368:96` ratio without stretching.
- The SVGs include built-in padding. Do not crop the viewBox; keep at least one stroke-width of space around the artwork. The existing standalone icons have a minimum margin of about one stroke width.
- The vermilion is a brand accent and should not be used as UI body text or business status color.
- Keep the gap between the two slices; do not add strokes, shadows, or gradients.

## Product Integration

Place the needed assets in the frontend's static resource directory (`web/public/`); the console build copies them into the served bundle.

```html
<img src="/brand/logo.svg" width="32" height="32" alt="Go TaaS" />
<img src="/brand/wordmark.svg" width="184" height="48" alt="Go TaaS" />
```

Choose the regular or `-dark` version based on the product's actual theme, not just the OS theme. Use an empty `alt` for purely decorative icons to avoid duplicating the adjacent product name in screen readers.

`logo-mono.svg` uses `currentColor` and inherits the page color only when inlined as SVG; when referenced via `<img>` it renders black by default. For dynamic monochrome icons, use a CSS mask:

```css
.go-taas-mark {
  display: inline-block;
  width: 32px;
  height: 32px;
  background-color: currentColor;
  mask: url("/brand/logo-mono.svg") center / contain no-repeat;
}
```

The PNGs are exported from the corresponding SVGs using resvg. Production icons and wordmarks require no external fonts, scripts, or remote resources; the preview keeps editable text.

This is a newly drawn visual design for the project and has not been trademark-searched; a similarity search should be completed before registering a trademark externally.
