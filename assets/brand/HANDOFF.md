# Handoff: k8s-dencer application icon

## Overview
Four candidate icon directions for **k8s-dencer**, a Kubernetes consolidation *planner*: it computes a
pod-repacking plan (respecting PodDisruptionBudgets, topology spread, affinity, taints, single-replica
workloads) and stops — decision support that can execute, not an autoscaler. Primary uses: **GitHub repo /
org avatar** and **web app favicon**. One direction should be picked, then the icon is exported at the
sizes below.

## About the design files
`preview/k8s-dencer Icon.dc.html` is a **design reference** — a comparison sheet, not production code.
The production artefacts are the standalone files in `svg/`. Use those; do not port the HTML sheet.

## Fidelity
**High-fidelity.** Geometry, colours and stroke weights are final. All coordinates below are in a
512×512 user-unit viewBox and scale losslessly.

## The system (shared by all four)
A lowercase **d** monogram: a rounded vertical stem plus a **seven-sided (heptagonal) bowl**, inside a
circular container. Seven sides and pod-dots read as Kubernetes-ecosystem without reproducing any
existing logo — no helm/wheel, no official mark, no trademarked element.

Shared geometry:
- Container: `circle cx=256 cy=256 r=256` (full-bleed circle; the disc IS the icon boundary).
- Bowl heptagon (1a/1b), vertex-up, radius 118 about (238, 292):
  `238,174 330.3,218.4 353,318.3 289.2,398.3 186.8,398.3 123,318.3 145.7,218.4`
- Stem: `rect x=332 y=96 w=40 h=322 rx=20` (1a/1b/1c) — bottom of stem aligns with bottom of bowl.

## Options
### 1a — Heptagon bowl  (recommended default)
Solid blue disc, white stem, white stroked heptagon bowl (stroke 40, round joins). One letter, one shape;
the most legible of the four at 16 px. Files: `icon-1a-heptagon-bowl.svg`, `icon-1a-mono.svg`.

### 1b — Seven pods
Navy disc; the bowl becomes seven cyan dots (r=27) on the heptagon vertices, bound by a 9-unit heptagon
ring at 40% opacity (the ring is what stops it reading as a loading spinner). Most literal
"workloads on nodes"; dots begin to merge at 16 px. Files: `icon-1b-seven-pods.svg`, `icon-1b-mono.svg`.

### 1c — The planned move
Blue disc; heptagon bowl (radius 100 about (240, 291), stroke 34) with a centre dot, plus one amber pod
outside the bowl on a **dashed** path inward — dashed because the move is *proposed, not executed*. The
only direction that carries the product thesis. Loose pod: `circle cx=108 cy=140 r=26`; path:
`line 130,166 → 192,238, stroke-width 16, dasharray 4 32, round caps`.
Files: `icon-1c-planned-move.svg`, `icon-1c-mono.svg`.

### 1d — Light / outline
Inverted weight: white disc, blue mark (heptagon radius 118 about (238, 289), stroke 32; stem w=34), plus
a 16-unit blue ring at 18% opacity so the disc edge survives on light page backgrounds. Quieter in a docs
sidebar; weakest as a GitHub avatar. Files: `icon-1d-light-outline.svg`, `icon-1d-mono.svg`.

## Design tokens
| Token | Hex | Used for |
| --- | --- | --- |
| Blue | `#2A5FE0` | 1a/1c disc, 1d mark |
| Navy | `#17284A` | 1b disc |
| Cyan | `#46C2E8` | 1b pods + ring |
| Amber | `#EFB324` | 1c proposed move |
| White | `#FFFFFF` | mark on dark, 1d disc |
| Ink | `#0B1220` | monochrome disc, wordmark text |

Wordmark / UI type: **IBM Plex Sans** 600 for the name (letter-spacing −0.02em), **IBM Plex Mono** 400 for
the tagline and any code-adjacent label. Suggested tagline: "consolidation plans, before they run".

Lockup: icon at 64 px, gap 18 px, name at 30 px/600 baseline-aligned with the icon centre; stacked
variant = icon above name, gap 14 px, centred.

## Export set to produce once a direction is picked
- `favicon.svg` (the chosen colour SVG) + `favicon.ico` at 16/32/48.
- PNG: 16, 32, 48, 64, 128, 256, 512, 1024. GitHub org avatar wants ≥ 500×500.
- Monochrome SVG for single-colour contexts (README badges, print).
- Optional maskable PWA icon: same mark on the blue disc with 10% safe padding (mark scaled to 80%).

## Small-size rules
- Never render below 16 px. At 16–20 px use the chosen colour SVG unchanged — strokes are ≥ 32 units
  (≈ 1 px at 16 px) by design.
- Do not add an outer border, drop shadow, or rounded-square plate; the circle is the container.
- Do not recolour the mark to a gradient, and do not place 1d on a white background without its ring.

## Files
- `svg/` — 8 standalone production SVGs (4 directions × colour + monochrome), 512×512 viewBox.
- `preview/k8s-dencer Icon.dc.html` — the comparison sheet (reference only; open in a browser).
