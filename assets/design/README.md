# Handoff: k8s-dencer product UI

## Overview
A redesign of the k8s-dencer web UI. k8s-dencer computes a Kubernetes consolidation plan — which pods to
move so nodes can be reclaimed — and **stops**. It is decision support that can execute, not an
autoscaler. The primary user is a **platform / SRE engineer deciding whether to approve a plan**, in a
context where an unplanned eviction is an incident.

**Direction 1a ("The ledger") is the chosen direction — build that one.** Twelve screens are delivered, dark and light. Direction 1b is retained in the
preview file for reference only; do not implement it as a screen.

## About the design files
`preview/k8s-dencer UI.dc.html` is a **design reference**, not production code: it is a design-tool
document (custom `<x-dc>` / `<sc-for>` runtime, all styling inline) holding both directions on one
canvas. Open it in a browser to see the intended pixels. **Do not port its markup** — reimplement in the
project's real stack (React + whatever CSS solution the repo already uses). Treat it the way you would a
Figma file.

Reference screenshots of the **existing** UI are in `current_ui/` for comparison.

## Fidelity
**High-fidelity for layout, spacing, type, colour, and copy.** Data is realistic but fabricated (node
names, PDB names and step counts come from the demo cluster in the screenshots). Interaction is *not*
built — the mockups are static; behaviour is specified in prose below.

## The two directions
### 1a — "The ledger" — CHOSEN, implement this
Leads with the decision. Structure, left to right:
- **Left rail, 224px.** Logo lockup, nav: Review (badge = step count) / Cluster / Recommendations (badge =
  high-severity count) / History. Bottom: cluster switcher + service-account row.
- **Top bar, 60px.** Plan name + short plan hash, freshness dot ("Fresh · computed 41s ago"), algorithm
  name, Recompute, settings.
- **Hero band.** Eyebrow "If you approve everything safe", then `Reclaim 3 of 24 nodes` **now**, with
  "14 reclaimable if the held-back rules are resolved" as secondary. Three stats: cores returned, memory
  freed, pods rescheduled. Right: a 3/4/7 proportional triage bar over three cards — Safe now / Needs a
  call / Held back.
- **Main region.** The **step list is the screen** (not a sidebar). Toolbar: selection count, verdict
  filters, "execution order" hint. Rows grouped under three verdict headers, each header carrying a
  one-line explanation of what the verdict means. Row grid:
  `26px checkbox | 30px order | 1fr node+pool | 96px pods | 78px verdict | 1fr reason | 16px chevron`.
  Safe rows are checked by default; Held-back rows render the node name in muted grey. The list scrolls,
  with a bottom fade + "N more steps below".
- **Detail pane, 392px.** For the focused step: verdict + step number, node name + pool, a plain-language
  paragraph of what the risk actually is, the **Safety Guard** check list (PDBs, topology spread,
  affinity, taints, single-replica workloads, destination capacity — each with a verdict glyph and a
  value), **Where the pods go** (pod → target node, flagged when it is the only replica), then
  "Add to selection" / "Skip" and the note that adding a step also adds a rollback point.
  This pane must clip its own overflow: the checks list and action block are fixed, the pods list is the
  compressible `flex:1 1 0; min-height:0; overflow:hidden` region.
- **Sticky footer, 68px.** Selection summary + what happens on run ("Safety Guard re-runs per step; the
  run halts on the first refusal"), then Copy as YAML / Rehearse / **Drain N nodes** (one filled button).

### 1b — "The packing" — NOT CHOSEN, reference only
Leads with the shape of the change. Top nav instead of a left rail. Hero: `24 nodes today → 21 safely,
10 at optimum`, then **two side-by-side packing panels** (Now / After the 3 safe steps), each a
8-column grid of 24 node tiles with a fill bar for utilisation. Colour legend: pods staying put, pods
arriving (accent), node reclaimed (green tile), held back by a rule (red tile). Below: the step ledger in
compact form on the left, and a **"What is blocking the rest"** column on the right — one card per rule
with severity tag, rule path, plain-language explanation, the fix, and "unblocks N steps".

**Carry these two parts of 1b forward into 1a's other destinations:** the "What is blocking the rest"
cards (severity tag, rule path, plain-language cause, the fix, "unblocks N steps") are the intended design
for the **Recommendations** destination in 1a's left rail; the before/after packing grid belongs in the
**Cluster** destination as one of its lenses. Neither appears on the plan-review screen itself.

## Design tokens
| Token | Hex | Role |
| --- | --- | --- |
| canvas | `#06080C` | app background |
| surface | `#0A0D13` | chrome: rails, bars, panes |
| surface-alt | `#0C0F16` | cards on canvas |
| raised | `#151A24` | chips, segmented-control active |
| inset | `#0F131B` | list items, tiles |
| border | `#1A1F29` | chrome dividers |
| border-strong | `#1E2430` / `#2A3346` | card borders / interactive borders |
| hairline | `#12161F` | table row separators |
| text | `#E8ECF3` | primary |
| text-2 | `#C6CEDB` | body in dense panes |
| text-3 | `#98A2B3` | secondary |
| text-4 | `#818C9E` | tertiary / row reasons |
| text-5 | `#667085`, `#4E5A6E` | metadata, mono labels |
| accent | `#4C7DFF` | primary action, selection, arriving pods |
| accent-text | `#7FA0FF` / `#B9CBFF` | links, active nav |
| green | `#3DD68C` (bg `#0F1A16`, border `#1D3A2C`, text `#7FCFA8`) | Safe |
| yellow | `#F5B544` (bg `#1A1509`, border `#3D2F13`, text `#D2A85C`) | Needs a call |
| red | `#F2545B` (bg `#1C0F11`, border `#40201F`, text `#D98A8D`) | Held back |

Every pair clears WCAG AA (body 8.9:1, secondary 5.2:1, all verdict colours ≥ 4.6:1 on surface). The
existing build sits near 1.6:1 in places (grey load bars on near-black) — that is the main accessibility
fix.

**Type.** IBM Plex Sans for prose and numbers-in-prose; IBM Plex Mono for every identifier (node names,
pod names, hashes, rule paths), all-caps eyebrows (letter-spacing 0.13–0.14em, 10.5–11px), and metric
values. Headings 30–34px/600/-0.025em. Body 13–16px. Never below 11px.

**Radii** 5–8px inside panels, 10–14px for cards, 12px for the app frame. Frame 1440×940.

## Deliberate changes from the current UI (keep these)
1. **One primary action.** Run / Dry run / Run to optimum competed as three equal buttons. Now: one filled
   button whose label reflects the live selection ("Drain 3 nodes"), "Rehearse" as its outlined sibling,
   "Run to optimum" demoted to an overflow menu. Rationale: the destructive path must be unambiguous.
2. **The step list is the primary object,** not a 320px sidebar showing 4 of 14 steps.
3. **Every verdict explains itself inline.** "7 held back" was a number with no path to the reason; each
   held-back step now names the rule and the remedy.
4. **Rack / Wells / Panel became lenses,** not top-level views — they were the same node data three ways
   at equal weight. One "Cluster" destination with a lens switcher.
5. **The headline states what approval buys** ("Reclaim 3 of 24 now, 14 if the held-back rules resolve")
   rather than an optimum the user cannot actually run today.
6. **Recommendations are tied to steps** ("unblocks 3 steps"), so 29 high-severity findings become an
   ordered work queue instead of a list.

## Interaction spec (not built in the mockup)
- Row click focuses the step in the detail pane; checkbox toggles selection independently.
- Verdict filter chips are single-select over the grouped list; group headers remain visible.
- Selection footer is live: label, pod count, and the copy under it recompute from the selection. If the
  selection contains any non-Green step, the button turns amber-outlined and requires typed confirmation
  of the node count.
- **Rehearse** = the existing rehearsal dialog: nothing cordoned or evicted, Safety Guard runs in full,
  same trail as a real run. Keep that copy — it is good.
- Execution: stream per-step progress into the same rows (cordon → evict → verify → reclaimed), halt on
  the first Safety Guard refusal, and surface the rollback point. A run must be abortable mid-plan.
- Freshness: if the live cluster diverges from the plan's snapshot, mark the plan stale in the top bar and
  disable the primary action until Recompute.
- Empty state: when nothing is safely reclaimable, the hero states that plainly and the screen leads with
  the blocking rules rather than showing a zeroed plan.

## The full screen set
Twelve frames, all 1440×940, all in 1a's system. Badge ids match the preview file.

| id | Screen | Purpose |
| --- | --- | --- |
| **1a** | Plan review — the ledger | **The core screen.** Spec above. |
| 1b | Plan review — the packing | Not chosen; its parts feed 2d and 3b. |
| **2a** | Rehearsal result | "Nothing was cordoned. Nothing was evicted." 18/18 checks, per-step outcome, full trail, and the approve path straight into a real run. |
| **2b** | Execution in progress | Review rows become progress rows in place, each with cordon → evict → verify → reclaim phase chips. Live streaming trail. Abort sits in the top bar, reachable at every step boundary; "Pause after this step" in the footer. |
| **2c** | Run halted by the Safety Guard | The trust screen. States what drifted between plan and step, what was kept, that the cordon was reverted and no pod was evicted for the failed step, and offers exactly two ways forward (wait / change the rule). **Deliberately offers no force-drain.** |
| **2d** | Recommendations | 1b's blocking-rule cards as a queue ranked by nodes unlocked. Detail pane: affected workloads, a YAML patch diff to copy, "Open the 3 blocked steps". Muting removes a finding from the queue, never from the plan. |
| **2e** | History | The estate chart redrawn as needed (grey) vs spare (amber) stacked bars — legible, unlike the near-black original — over an audit ledger: when, plan hash, steps run, nodes reclaimed, outcome, **authorised by**, trail link. |
| **3a** | Sign in | Cluster context from kubeconfig; OIDC primary, service-account token fallback, read-only session checkbox. Left panel carries the lockup (76px icon + halo, 36px wordmark), the line "It produces the plan and stops", and an ambient loop of pods leaving two nodes and arriving on three. |
| **3b** | Cluster — Rack lens | The nodes-and-pods screen. Pods drawn *inside* each node card, width = CPU request, coloured by plan intent (safe / needs a call / held back / stays). Detail pane lists each pod with its destination and flags the only-replica case. |
| **3d** | Cluster — Wells lens | The fill metaphor, given the job the other lenses do not do: **destinations**. Each well is one node's allocatable CPU — grey already requested, accent arriving from the plan, dashed amber line the 85% packing ceiling k8s-dencer will not plan above (which is why nodes with visible room receive nothing). Nodes tagged receiving / has room / near limit / emptying / held back. Right pane lists all six pours with the request each adds and the worst-case node after the plan lands. |
| **3c** | Cluster — Load lens | The Panel screen rebuilt. Each bar splits **used** (accent) vs **reserved but idle** (amber), sorted by that gap, plan verdict in the last column. Headline states the finding: pods reserve 2.4× the CPU they use. |
| **4a** | Plan review, light theme | 1a in the light palette. Identical grid, copy and hierarchy; only the surface stack and verdict values change. |

**Cluster lenses.** Rack / Wells / Load are lenses on one Cluster destination, switchable from both the rail
and the header — not four top-level views of the same data. **Selection is shared between Cluster and
Review:** selecting a node selects its step, and vice versa. Each lens answers a different question and none
is redundant: Rack = what is on each node, Wells = where displaced work lands and whether it fits, Load =
where the gap between requested and used CPU is.

## Light theme
Both themes are first-class; the toggle sits in the top bar. **Do not derive light by inverting dark.**

| Token | Dark | Light |
| --- | --- | --- |
| canvas | `#06080C` | `#F4F5F7` |
| surface (chrome) | `#0A0D13` | `#FFFFFF` |
| surface-alt (detail pane) | `#0C0F16` | `#FAFBFC` |
| raised (chips, active seg) | `#151A24` | `#EEF0F4` |
| inset (list items) | `#0F131B` | `#FFFFFF` + 1px border |
| border | `#1A1F29` | `#E3E7EC` |
| border-strong | `#2A3346` | `#C2CAD6` |
| hairline (row rules) | `#12161F` | `#EDF0F4` |
| group-header band | `#080B10` | `#F7F8FA` |
| text | `#E8ECF3` | `#0B1220` |
| text-2 | `#C6CEDB` | `#2C3542` |
| text-3 | `#98A2B3` | `#55606F` |
| text-4 | `#818C9E` | `#6B7684` |
| text-5 | `#667085` | `#8A93A0` |
| accent | `#4C7DFF` | `#2A5FE0` |
| accent-text | `#7FA0FF` | `#1F4CBF` |
| nav active bg | `#18203A` | `#E8EEFC` |
| green | `#3DD68C` | `#17875A` (bg `#E9F6EF`, border `#BFE3D0`, text `#0E6B46`) |
| yellow | `#F5B544` | `#9A6400` (bg `#FDF3E0`, border `#F0DBAE`, text `#7A4F00`) |
| red | `#F2545B` | `#C0343C` (bg `#FDECEC`, border `#F3C9CB`, text `#9E262E`) |

Two rules that a mechanical inversion gets wrong:
1. **The verdict colours are redrawn, not flipped.** `#F5B544` amber is 1.9:1 on white — unreadable. The light
   set runs 4.6–5.9:1 as text and preserves the same hue order, so a screenshot in either theme reads the same.
2. **Elevation reverses direction.** Dark: raised = lighter fill. Light: raised = whiter + 1px border, **no
   shadow**. Never port dark's filled tiles onto light.

Only 4a is drawn in light; derive the other screens from this table.

## Motion
One animation exists, on 3a: a 6s `ease-in-out infinite` loop over a 5-node strip — pods on the last two nodes
lift and fade (`dcr-out`), blue arrivals fade in on the first three (`dcr-in`, staggered 0.25s), and the emptied
tiles and their status dots transition to green (`dcr-free`, `dcr-dot`). Keyframes are in the preview file's
`<style>` block. Respect `prefers-reduced-motion` — render the end state statically.

Elsewhere: no decorative motion. Progress states (2b) animate because they reflect real work. Verdict
colour changes must NOT animate — a step going from Safe to Held back has to read as a state, not a
transition.

## One layout rule that bit us twice
In the 392px detail pane, **the payload list must size to its content and the uniform list must be the scroll
region.** "Where the pods go" is `flex: 0 0 auto`; the Safety Guard check list is
`flex: 1 1 0; min-height: 0; overflow: hidden` with a fade at its edge. Making the pods list flexible silently
dropped its third row — the pod carrying the `only replica` flag, i.e. the entire reason that step is Caution
rather than Safe. A clipped row in a 6-row uniform list reads as "scroll"; a missing row in a 3-item payload
reads as "placed elsewhere". Any clipping region needs a fade plus an explicit "+N more" affordance.

## Copy rules
The voice is plain and load-bearing. Keep these verbatim:
- "It produces the plan and stops."
- "Nothing will be cordoned or evicted. The Safety Guard runs in full and you get the same trail you would
  see for a real run." — this is the existing rehearsal copy and it is already right.
- "The Safety Guard re-runs before every step."
- "There is no override in this UI. A refusal is the product working."
- Verdict labels are **Safe now / Needs a call / Held back**. Green/Yellow/Red are internal severities; the
  UI says what they mean. Never label a control with a colour.
- Never use "optimum" as a call to action — it is a ceiling, not a plan you can run today.

## Not yet designed
Wells lens, light theme, responsive behaviour below ~1200px, settings, multi-cluster, notifications, and the
empty / stale-plan states described in the interaction spec. Ask before inventing them.

## Files
- `preview/k8s-dencer UI.dc.html` — all twelve frames on one canvas (open in a browser).
- `current_ui/` — screenshots of the UI being replaced.
