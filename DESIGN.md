---
name: Werkt
description: A precise operational workspace for code automations.
colors:
  ink: "oklch(0.19 0.018 43)"
  ink-soft: "oklch(0.34 0.018 43)"
  muted: "oklch(0.47 0.018 43)"
  faint: "oklch(0.64 0.014 43)"
  line: "oklch(0.89 0.009 43)"
  canvas: "oklch(0.982 0.003 43)"
  surface: "oklch(1 0 0)"
  surface-selected: "oklch(0.955 0.032 47)"
  primary: "oklch(0.59 0.205 42)"
  primary-hover: "oklch(0.52 0.2 42)"
  success: "oklch(0.47 0.13 151)"
  error: "oklch(0.5 0.19 25)"
  info: "oklch(0.42 0.095 205)"
typography:
  headline:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "1.5rem"
    fontWeight: 700
    lineHeight: 1.2
    letterSpacing: "-0.035em"
  title:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "1.125rem"
    fontWeight: 700
    lineHeight: 1.25
    letterSpacing: "-0.025em"
  subtitle:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 700
    lineHeight: 1.25
    letterSpacing: "-0.025em"
  brand:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "1.05rem"
    fontWeight: 780
    lineHeight: 1.5
    letterSpacing: "-0.025em"
  body:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.5
  body-small:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.5
  control:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 700
    lineHeight: 1.5
  label:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 700
    lineHeight: 1.5
  micro:
    fontFamily: "-apple-system, BlinkMacSystemFont, Segoe UI, system-ui, sans-serif"
    fontSize: "0.6875rem"
    fontWeight: 700
    lineHeight: 1.5
  revision:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace"
    fontSize: "0.625rem"
    fontWeight: 400
    lineHeight: 1.5
  mono:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.65
rounded:
  sm: "0.25rem"
  md: "0.5rem"
  lg: "0.75rem"
spacing:
  1: "0.25rem"
  2: "0.5rem"
  3: "0.75rem"
  4: "1rem"
  5: "1.5rem"
  6: "2rem"
  7: "3rem"
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.surface}"
    rounded: "{rounded.md}"
    padding: "0.5rem 0.85rem"
    height: "2.5rem"
  button-primary-hover:
    backgroundColor: "{colors.primary-hover}"
    textColor: "{colors.surface}"
    rounded: "{rounded.md}"
  button-quiet:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink-soft}"
    rounded: "{rounded.md}"
    padding: "0.5rem 0.85rem"
  inventory-selected:
    backgroundColor: "{colors.surface-selected}"
    textColor: "{colors.ink}"
    rounded: "{rounded.md}"
    padding: "0.5rem 0.75rem"
---

# Design System: Werkt

## 1. Overview

**Creative North Star: “The Bench Instrument”**

Werkt is a precise working surface: bright, compact, and designed around readings and controls rather than decoration. Its desktop topology is a persistent navigation rail, an organized inventory, a wide detail surface, and an optional diagnosis pane. On smaller screens those same responsibilities become focused views instead of a miniature desktop.

The interface is direct, assured, and useful. It rejects visual workflow canvases, generic KPI-card dashboards, cyberpunk agent consoles, vague friendly abstractions, and the inflated radii or gradient-heavy sameness common to generated interfaces.

**Key Characteristics:**

- Dense enough for operational work, with calm whitespace between responsibilities.
- Flat white and warm-neutral surfaces separated by one-pixel rules.
- Orange is reserved for selection, primary action, and focus.
- Exact revisions, event IDs, attempts, logs, and output remain visible system truth.
- Responsive changes preserve the task hierarchy rather than merely shrinking it.

## 2. Colors

The palette pairs warm paper neutrals with a single burnt-orange action color and unambiguous semantic states.

### Primary

- **Signal Orange** (`oklch(0.59 0.205 42)`): primary actions, selection marks, active tabs, and focus.
- **Deep Signal Orange** (`oklch(0.52 0.2 42)`): hover state only.

### Neutral

- **Instrument Ink** (`oklch(0.19 0.018 43)`): primary text and decisive controls.
- **Soft Ink** (`oklch(0.34 0.018 43)`): table values and secondary controls.
- **Reading Gray** (`oklch(0.47 0.018 43)`): explanatory copy and labels.
- **Hairline** (`oklch(0.89 0.009 43)`): panel, row, and section separation.
- **Warm Canvas** (`oklch(0.982 0.003 43)`): application background and recessed regions.
- **Paper Surface** (`oklch(1 0 0)`): active working surfaces.

### Secondary

- **Healthy Green** (`oklch(0.47 0.13 151)`): successful and active state.
- **Failure Red** (`oklch(0.5 0.19 25)`): failed state and destructive feedback.
- **Running Blue** (`oklch(0.42 0.095 205)`): in-progress state.

### Named Rules

**The One Signal Rule.** Orange indicates the next useful action or the current selection; it is never ambient decoration.

**The Words-and-Color Rule.** Every semantic color is paired with a visible label or icon so color is never the sole carrier of state.

## 3. Typography

**Display Font:** system sans-serif stack
**Body Font:** system sans-serif stack
**Label/Mono Font:** `ui-monospace`, SFMono-Regular, Menlo, Monaco, Consolas

**Character:** Native system typography keeps the workspace fast and familiar. Weight and compact scale create hierarchy; monospaced text is used only where exact machine identity matters.

### Hierarchy

- **Headline** (700, `1.5rem`, 1.2): automation and global-view titles.
- **Title** (700, `1.125rem`, 1.25): pane titles and modal titles.
- **Section** (700, `1rem`, 1.3): trigger, run, and revision headings.
- **Body** (400, `1rem`, 1.5): base UI and longer empty/error copy, limited to roughly 65 characters where prose appears.
- **Label** (650–750, `0.6875rem`–`0.8125rem`, normal case): table headers, metadata, filters, and compact controls.
- **Mono** (400–700, `0.6875rem`–`0.8125rem`): revision hashes, event IDs, run IDs, logs, and output.

### Named Rules

**The Exactness Rule.** Use monospace for values an agent or operator may copy, compare, or search; do not use it as a visual theme.

## 4. Elevation

The workspace is flat by default. One-pixel rules and warm tonal shifts define structure; elevation appears only when a layer truly overlaps another layer.

### Shadow Vocabulary

- **Overlay edge** (`-8px 0 0 oklch(0.15 0.018 43 / 0.08)`): the responsive diagnosis drawer.
- **Dialog lift** (`0 8px 0 oklch(0.15 0.018 43 / 0.12)`): modal dialogs only.
- **Toast lift** (`0 6px 0 oklch(0.15 0.018 43 / 0.14)`): transient feedback only.

### Named Rules

**Flat by Default.** Do not put shadows around inventory rows, sections, tables, or ordinary containers. Overlap must be real before elevation is used.

## 5. Components

### Buttons

- **Shape:** compact rounded rectangle (`0.5rem`) with a minimum height of `2.5rem`.
- **Primary:** Signal Orange, white text, `0.5rem 0.85rem` padding; one clear primary action per local context.
- **Hover / Focus:** one-pixel upward movement on hover; a two-pixel orange outline plus offset on keyboard focus.
- **Quiet:** paper surface, hairline border, Soft Ink text.

### Chips

- **Style:** fully rounded filter controls with a one-pixel neutral border.
- **State:** unselected is white and muted; selected is Instrument Ink with white text.

### Cards / Containers

- **Corner Style:** panels remain square; only small interactive items and true overlays use radii.
- **Background:** Warm Canvas for recessed regions, Paper Surface for active work.
- **Shadow Strategy:** none at rest; see Elevation for genuine overlays.
- **Border:** one-pixel Hairline rules create the application topology.
- **Internal Padding:** primarily `1rem`, `1.5rem`, or `2rem` according to hierarchy.

### Inputs / Fields

- **Style:** Warm Canvas fill, one-pixel strong neutral stroke, `0.5rem` radius.
- **Focus:** Paper Surface fill, Signal Orange border, and a three-pixel translucent orange ring.
- **Error / Disabled:** explicit inline copy; disabled actions retain their label and reduce opacity.

### Navigation

- **Style:** compact icon-and-label rail on desktop, persistent bottom navigation on mobile.
- **State:** selected items use the pale orange surface plus a short Signal Orange underline. Hover uses only a neutral tonal shift.

### Run Diagnosis

The diagnosis pane is the signature component. It keeps run identity, lifecycle state, attempt count, exact error, logs, and output together. It is a fourth desktop pane, a side drawer at tablet widths, and a focused full-screen surface on mobile.

## 6. Do's and Don'ts

### Do:

- **Do** use the same public management API for agents and the workspace.
- **Do** lead with current lifecycle state and the next safe action.
- **Do** preserve exact revision, run, event, and attempt data.
- **Do** keep primary controls at least `2.5rem` high and coarse-pointer controls at least `2.75rem` high.
- **Do** reflow dense tables into labeled rows on narrow screens.
- **Do** respect reduced motion and retain visible keyboard focus.

### Don't:

- **Don't** introduce node-canvas workflow builders that make code look secondary.
- **Don't** assemble generic SaaS KPI dashboards from interchangeable metric cards.
- **Don't** use cyberpunk agent-console styling, neon gradients, glass panels, or ornamental terminals.
- **Don't** invent friendly but vague abstractions instead of showing revisions, triggers, runs, and errors directly.
- **Don't** fall into AI-generated sameness: inflated radii, floating cards, purple-blue gradients, or decorative automation diagrams.
- **Don't** use semantic color without a visible textual state.
