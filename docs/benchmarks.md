# Measured ceiling

Where k8s-dencer stops being usable, measured rather than estimated.

Produced by `make bench`, which runs entirely offline over synthesised clusters
— `internal/model` has no Kubernetes imports, so the whole pipeline can be
measured at thousands of pods without standing anything up.

Apple M-series laptop, Go 1.26, `-benchtime 1x`. Absolute numbers will differ on
other hardware; **the growth curves are the point.**

## Baseline — before any Phase 4 optimisation

| Stage | 119 pods | 916 pods | 2,526 pods | 5,026 pods | Growth |
|---|---|---|---|---|---|
| `constraints.Analyze` | 1.6 ms | 190 ms | **4.4 s** | **33.5 s** | **≈ cubic** |
| `planner.Greedy.Plan` | 0.17 ms | 77 ms | **1.5 s** | — | **≈ cubic** |
| `impact.ClassifyPlan` | 0.04 ms | 2.2 ms | 17 ms | — | ≈ quadratic |
| `graph.Build` + marshal | 0.43 ms | 2.5 ms | 11 ms | 28 ms | ≈ linear |
| `store.Save` | 0.74 ms | 5.3 ms | 29 ms | 83 ms | ≈ linear-ish |
| `constraints.NewPlacement` | 0.06 ms | 0.09 ms | 0.19 ms | 0.74 ms | **linear** |

Graph payload size: 0.05 MB → 0.44 MB → 1.17 MB → 2.30 MB. Linear, ~0.46 KB per
pod.

## What this means

**The usable ceiling today is roughly 1,000–2,000 pods.** At 2,500 pods a single
analyse-and-plan cycle costs ~6 seconds, against a default 30-second resync. At
5,000 pods the analyser alone takes 34 seconds and the planner never gets a
coherent turn.

Extrapolating the cubic term to the 50,000-pod target gives **hours per
analysis**. That is not a tuning problem.

## Where the cubic comes from

`constraints.Analyze` is 69% `Placement.domainCounts`, of which 44% is
`LabelSelector.Matches` (CPU profile at 2,526 pods).

`domainCounts` walks **every node and every occupant**, running a label-selector
match, to count how many matching pods sit in each topology domain. It is called
once per spread-constrained pod per candidate node — so
`pods × nodes × pods`.

The counts do not vary per candidate node. They depend only on the selector, the
topology key, and the current placement, so they can be computed once and
memoised. That is M18's job.

## What was expected, and what was actually true

The Phase 4 plan predicted the planner's four-deep nesting in `greedy.go` would
be the first wall. It is a wall, but **the analyser hits it sooner** — and the
shared cause is in `internal/constraints`, not `internal/planner`.

Worth recording because it is the reason M16 exists: the fix that looked obvious
from reading the code would have optimised the wrong package.

## Running it

```bash
make bench                  # up to ~2,500 pods, about 40 seconds
SCALE_LARGE=1 make bench    # adds the 5,000-pod point, several minutes
```

Sizes stop at 2,500 on purpose. A benchmark nobody will sit through is a
benchmark nobody maintains; the larger points are opt-in until the growth curve
is fixed.
