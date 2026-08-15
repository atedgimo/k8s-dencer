# What to look at in the UI, and what "right" looks like

The other checklist asks whether the product works. This one asks whether it
**says true things**, which is a different question and the one the UI is for.

Written because the last three UI bugs — a fix queue that reported 0 findings
on a cluster with 34, a rationale that printed the same sentence twice, a
footer that called an empty selection "All Green" — all passed every unit
test, every gate, and a screenshot review. They only appeared when someone
drove the real thing and read it.

Open the UI with the URL the launcher prints. Auth is on: paste the token it
gives you.

---

## The one that matters most

**Everything below is downstream of this: the plan must have steps.**

On 2026-08-07 this product could not plan on GKE at all — every node read as
undrainable because a static pod owned by the node was treated as movable. If
Review shows `0 steps` and Cluster shows nodes that are clearly half empty,
**stop and capture**; that is the P0 returning and nothing else is worth
measuring.

---

## Review

| Look at | Right | Wrong, and what it means |
|---|---|---|
| Headline | Names a decision — "N nodes can be reclaimed safely" or, if nothing is safe, what is blocking | A number with no verdict |
| Triage counts | Safe + Caution + Held back equals the step count in the rail badge | They disagree — two views of one plan drifting |
| A step's rationale | One sentence per distinct reason | **The same sentence twice.** Fixed in v0.8.1; if it is back, the dedup regressed |
| Footer with nothing selected | "Nothing selected. Choose the steps you want to run." | "All Green" — fixed in v0.8.1 |
| Footer with a Red step selected | Demands a typed confirmation | Lets you run it silently |

## Recommendations

**Check this within ten seconds of signing in.** Three hooks used to fetch
before a token existed, swallow the 401, and then wait a full minute — so the
screen said "No findings against the current cluster" while there were 34.
Fixed in v0.8.1, and this is the cheapest place to catch it coming back.

- rail badge and the page header must agree on the high-severity count
- a finding with a fix shows paste-ready YAML
- "Blocking a step" is often empty on a healthy cluster; **that is correct** —
  switch to "All findings" before concluding anything

## Resilience *(new in v0.8.0)*

- explanations are the analyzer's own words, identical to what `dencer audit`
  prints
- **DaemonSet pods must not appear.** They cannot move, but losing their node
  costs nothing. If `ControllerPinned` findings are listed, the pinned-vs-
  at-risk distinction regressed
- kinds read as words — "PDB Zero Headroom", not "PDBZEROHEADROOM"

## Cluster

Three lenses, and the interesting one on GKE is **Load**.

- **Load**: on OrbStack this shows one node, because 17 of 18 are simulated and
  have no kubelet for metrics-server to scrape. **On GKE every node is real, so
  every node should appear.** If nodes are missing there, that is a genuine
  bug — and the headline must say "On the N measured nodes", never claim the
  ratio for the whole fleet
- **Wells**: the dashed line is the packing ceiling the planner actually used
  (`planner.packCeiling`, default 0.85). Bars should not cross it
- **Rack**: pods drawn in situ, width by CPU request

## Rightsizing

- refuses to guess: with no usage source it says so rather than estimating
- on GKE with `usageSource=metrics-server` it should list real workloads
- rows for workloads that also have a finding show "finding open"

## History

- the ledger fills only after a drain **and** a reclamation
- money appears only if the launcher passed prices — it does, from the same
  cents/hour the consent line quoted
- **unpriced is not free.** If a machine type has no price it must be reported
  as unpriced, never as zero

## Both themes

Toggle it. The verdict colours are redrawn per theme rather than inverted, so
light is not a tint of dark and has to be looked at separately.

---

## While you are playing

Worth trying, in rough order of how much they teach:

1. **Select a Caution step and Rehearse.** A dry run executes every guard check
   and emits the same events without cordoning or evicting anything.
2. **Then drain it for real** and watch the run screen: cordon → evict → verify
   → drained. Then check History and Resilience again — the estate changed.
3. **Open a Red step.** It should refuse without a maintenance window, and say
   so as the product working rather than as an error.
4. **Try `dencer` from your terminal** against the same cluster; the CLI and the
   UI must agree. Constraint explanations are printed verbatim in both
   precisely so they cannot disagree.
5. **Break something on purpose** — scale a Deployment to 1 replica, or add a
   zero-headroom PDB — and watch it appear in Recommendations and Resilience.

If anything reads oddly, `./hack/capture/capture.sh <phase>` takes the whole
snapshot. Better a capture you throw away than a memory of something strange.
