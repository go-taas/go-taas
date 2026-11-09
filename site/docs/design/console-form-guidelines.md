# Console Form Guidelines — Requirement Tiering and Section Layout

| Attribute | Content |
| --- | --- |
| Feature point | Console form design system (every create/edit form on both surfaces) |
| Document scope | Requirement analysis and interaction design for how a console form states what it needs: the three requirement tiers, the legend, numbered sections, the outstanding-fields row, and the shared primitives that render them |
| Owning modules | `web` (console), `docs` (this document) |
| Related documents | [Model Catalog & One-Click Deployment](./model-catalog-deployment.md) — FR3.5, the create-service form this pattern was first built for |
| Status | Implemented |

---

## 1. Background

Creating a resource on the platform is not one decision. Deploying an inference
service asks for roughly twenty-five inputs; registering a model asks for five,
allowing an image import asks for eight, a webhook for six. Before this
convention the forms said nothing about which of those inputs the platform
actually needs, so an operator learned the answer the expensive way: fill in what
looks plausible, submit, read the error, come back.

That is a bad trade for three reasons.

- **It hides the cheap case.** Most of those inputs already carry the value the
  platform would choose. Deploying a model with the fleet's defaults requires
  exactly one typed field — the service name — and the form gave no hint that
  this was true.
- **It punishes the wrong fields.** A form that highlights nothing makes an
  operator double-check the twelve fields that were already correct.
- **It hides the risk.** Two fields that look alike can differ sharply: leaving
  the engine's `max-model-len` empty is safe (the engine picks its own), while
  leaving `weightPath` empty is not (there is no default to pick). Only a
  per-field statement can carry that difference.

The same three facts drive the layout problem. A form with twenty-five inputs
scrolled as one wall: no grouping, no rhythm, no answer to "have I finished?".

## 2. Requirement

### 2.1 The three tiers

Every input on every console form belongs to exactly one tier, and the form says
which:

| Tier | Meaning | What the operator does |
| --- | --- | --- |
| **Required** | The request is rejected without a value. | Fill it in. Several are pre-filled from context (the model and version carried over from the catalog row that opened the form, the image the accelerator filter picked), so the count of fields actually typed by hand is small. |
| **Default** | A working value is already there: the platform default, a number the fleet reported, or a value derived from another field. | Nothing, unless this resource needs something else. The badge states the concrete value where one exists (`Default · 4`). |
| **Optional** | Empty is a valid answer. | Nothing, if the feature is not wanted: empty means "off" or "the engine keeps its own default". |

A derived value gets its own word — **Derived** — instead of a number, because the
number changes with other fields (a single-node replica's card count is
`TP × PP`) and a stale figure would be worse than none.

### 2.2 Requirements

- **R1** Each field renders its tier as a badge next to its label; the badge's
  tooltip explains the tier.
- **R2** The three tiers are explained once, at the top of the form, so the first
  badge is already understood when it appears.
- **R3** A badge states the concrete default when the field has one; it says
  "Derived" when another field decides the value.
- **R4** A form with more than one conceptual group is split into numbered
  blocks, each with a one-line summary of what the block decides and a hairline
  rule above it.
- **R5** The action row states what is still missing (`Still empty: Service name`)
  and turns green once nothing is, so a missing required value is visible before
  the round trip.
- **R6** In a dialog taller than the viewport the action row sticks to the bottom
  edge, so the outstanding-fields line is reachable without scrolling.
- **R7** A field's label carries no redundant "(optional)" suffix: the badge is
  the statement.
- **R8** The pattern comes from shared primitives, not from copy-paste, so a new
  form is consistent by construction.

## 3. Design

### 3.1 Primitives

`web/src/components/FormField.tsx` owns the vocabulary:

| Export | Renders |
| --- | --- |
| `ReqFlag` | The tier badge (red `Required`, blue `Default`, neutral `Optional`), with the tier explanation as its tooltip. |
| `FieldLabel` | A label plus its tier badge, bound to the input by `htmlFor`. |
| `FormLegend` | The three-tier legend shown once at the top of a form. |
| `FormSection` | One numbered block: step number, title, the block's own tier, a one-line hint, and the divider rule above it. |
| `RequiredStatus` | The action-row line naming what is still missing, or confirming nothing is. |

`Requirement` (`required` | `defaulted` | `optional`) is the type shared by all of
them, so a field cannot be silently unclassified when it is written with
`FieldLabel`.

The i18n keys live under `form.*` (`form.req.*`, `form.reqHint.*`,
`form.reqDefaultValue`, `form.reqDerived`, `form.requiredMissing`,
`form.requiredComplete`) rather than under the feature that first used them: the
wording is about forms, not about deploying a model.

### 3.2 Layout

```text
┌─ panel / dialog ───────────────────────────────────────────┐
│ Title                                          Cancel      │
│ ┌ legend ────────────────────────────────────────────────┐ │
│ │ [Required] …   [Default] …   [Optional] …              │ │
│ └────────────────────────────────────────────────────────┘ │
│ ①  Basics                                  what it decides │
│ ────────────────────────────────────────────────────────── │
│    Model [Required]              Version [Required]        │
│    …                                                       │
│ ②  Replicas and layout                     what it decides │
│ ────────────────────────────────────────────────────────── │
│ ③  …                                                       │
│ ────────────────────────────────────────────────────────── │
│ Still empty: Service name          [Cancel] [Deploy]       │
└────────────────────────────────────────────────────────────┘
```

The step number gives position and progress; the hint says what the block
decides, so the operator does not have to infer the grouping from the fields;
the rule separates them without a box-in-a-box. Badges are per field, not per
block — a block's tier is only a summary (a "required" block still contains
optional fields).

### 3.3 Which form gets what

| Form | Tiers | Sections | Status row |
| --- | --- | --- | --- |
| Create inference service (`DeployForm`) | yes | 6 | yes |
| Register model | yes | 2 | yes |
| Upload model | yes | 2 | yes |
| Download from a hub | yes | 2 | yes |
| Upload image / import image | yes | 2–3 | yes |
| Create engine parameter template | yes | 4 | yes |
| Create webhook (admin and user surfaces) | yes | 3 | yes |
| Create SSO provider | yes | 3 | yes |
| Create model provider | yes | 2 | yes |
| Create API key (admin and user surfaces) | yes | 2 | yes |
| Create vGPU pool | yes | 2 | yes |
| Create project / organization / identity binding | yes | — | yes |
| Add user to organization | yes | — | yes |
| Set a price | yes | 3 | yes |
| Autoscaling policy (global and per service) | yes | — | — |
| Organization settings | yes | — | yes |
| Service topology edit | yes | — | — |

A dialog with one conceptual group gets badges, legend and status row but no
numbered sections: numbering three fields would add ceremony, not clarity.

## 4. Acceptance Criteria

- **AC1** A required field is rejected by the server without a value, and its
  badge says `Required`.
- **AC2** A field with a platform default shows the value in its badge
  (`Default · 4`) and can be left untouched.
- **AC3** An optional field left empty submits successfully and produces the
  "off" or "engine default" behaviour the hint describes.
- **AC4** The legend appears once per form and explains all three tiers.
- **AC5** Every block of a multi-group form renders a divider above it and a
  one-line hint; the first block has no divider.
- **AC6** The action row reports each missing required field by name and switches
  to the complete state when the last one is filled.
- **AC7** In a dialog taller than the viewport the action row and its status line
  stay visible while the fields scroll.
- **AC8** No form label ends with a bare "(optional)" suffix.
- **AC9** Forms render without horizontal scrolling at 375 px and inside a 560 px
  dialog, and the two-column grid collapses as before.

## 5. Implementation Notes

- **Grid tracks.** `.form-grid` uses `minmax(0, 1fr)` rather than `1fr` and its
  controls carry `min-width: 0`. A bare `1fr` track refuses to shrink below its
  content's min-content width, so one long option label or placeholder used to
  push the second column past the container and give the dialog a horizontal
  scrollbar.
- **Sticky actions.** `.dialog > .dialog-actions` is `position: sticky` with
  negative offsets that cancel the dialog's padding, plus an opaque background
  and a top border: the row reads as a footer over the scrolling fields rather
  than as a floating strip.
- **Status clamping.** The status line is clamped to two lines so a form with six
  missing fields never pushes its own buttons onto a second row.
- **Colour is not the only signal.** Each badge carries its tier as text, so the
  distinction survives a colour-blind reader and a monochrome screenshot.
