# Measured ceiling

Where k8s-dencer stops being usable, measured rather than estimated.

Produced by `make bench`, which runs entirely offline over synthesised clusters
— `internal/model` has no Kubernetes imports, so the whole pipeline can be
measured at thousands of pods without standing anything up.

Apple M-series laptop, Go 1.26, `-benchtime 1x`. Absolute numbers will differ on
other hardware; **the growth curves are the point.**

## After M17 — memory and stored bytes

M18 made the planner fast enough; M17 addresses what it costs to *hold* and
*keep* a cluster this size. Three changes, each measured.

**Stored plans are gzipped** ([sqlite.go](../internal/store/sqlite/sqlite.go)).
JSON of a cluster snapshot is extremely repetitive, and the snapshot and
analysis BLOBs compress accordingly. The encoding is self-describing via the
gzip magic bytes, so no migration was needed and rows written before this change
still read back.

| Pods | JSON in | Stored on disk | Ratio | `Save` time |
|---|---|---|---|---|
| 119 | 0.10 MB | 5.3 KB | 19.1× | 1.5 ms |
| 916 | 1.00 MB | 54 KB | 19.2× | 4.7 ms |
| 2,526 | 4.44 MB | 197 KB | 23.1× | 18 ms |
| 5,026 | 14.1 MB | **609 KB** | **23.8×** | 54 ms |

The ratio improves with size, which is what you would expect: more pods means
more repetition of the same key names and label values. At the 50,000-pod target
this is the difference between a retention policy measured in gigabytes and one
measured in tens of megabytes.

Compressing did not cost time at the sizes that matter. Against the
pre-compression baseline below, `Save` is slower at 119 pods (0.74 → 1.5 ms) and
*faster* from 2,526 up (29 → 18 ms, 83 → 54 ms): past a certain size, writing
20× fewer bytes to SQLite more than pays for the gzip.

`BenchmarkScaleStoreSave` now reports the bytes actually on disk. It previously
reported the marshalled JSON length, which after this change would have
overstated retention by ~20× — the exact quantity the benchmark exists to
track.

**The informer cache is transformed on the way in**
([collector.go](../internal/cluster/collector.go)). Managed fields, pod
annotations and container statuses are discarded before anything is cached.

| | Heap per pod | 50,000 pods |
|---|---|---|
| Untransformed | 9,183 B | 459 MB |
| Transformed | 6,423 B | **321 MB** |

**30% off the planner's dominant memory cost** — 138 MB at the 50k target. The
number depends entirely on how large real managed fields are, so it was
calibrated rather than assumed: on the development cluster,
`kubectl get pods -A -o json --show-managed-fields` reports a mean 3,505 B of
managed fields on kubelet-run pods, about 40% of the serialised object. (The
flag matters — kubectl hides the section by default, which is why a first pass
measured 2 B and made the transform look worthless.)

The correctness risk here is stripping something the converter reads, so
`transform_test.go` asserts the property directly rather than by inspection: it
converts an object, transforms it, converts it again, and requires the two
domain objects to be identical. Three mutations were run against it — stripping
node annotations, pod conditions, and pod labels — and each fails the test.

**Reads are paginated and scoped**
([direct.go](../internal/cluster/direct.go)). Every List now pages at 500, and
the drain loop's "has this pod gone yet" poll — which listed every pod in the
cluster every 2 seconds to ask about one — is a single Get.

## After M18 — the spread index

`Placement.domainCounts` is memoised and maintained incrementally
([spreadindex.go](../internal/constraints/spreadindex.go)).

| Stage | 119 | 916 | 2,526 | 5,026 | Speedup at 5k |
|---|---|---|---|---|---|
| `constraints.Analyze` | 0.5 ms | 16 ms | **90 ms** | **298 ms** | **112×** |
| `planner.Greedy.Plan` | 0.16 ms | 17 ms | **110 ms** | **466 ms** | — |

**The growth curve is the real result.** Doubling from 2,526 to 5,026 pods used
to cost 7.6× the time; it now costs 3.3×. Cubic became roughly quadratic.

A full analyse-and-plan cycle at 2,526 pods went from **5.9 s to 0.2 s**, which
moves the usable ceiling from ~1–2k pods to comfortably past 5k against a
30-second resync.

Golden planner tests unchanged — byte-identical plans from the same fixture.

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
