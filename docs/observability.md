# Observability

Prometheus metrics, what each component publishes, and the assertions that keep the chart's monitors pointing at something real.


Every component serves Prometheus metrics at `/metrics` on the port it already
listens on. The path is a Go constant, `telemetry.MetricsPath`, and `make lint`
reads it out of the source and fails if the chart's monitors disagree.

That assertion exists because the chart used to ship a `ServiceMonitor`
scraping `/metrics` when **no component served `/metrics` at all**. A monitor
aimed at a 404 is worse than no monitor: it presents as a configured target
that is merely failing.

```bash
helm upgrade --install ... --set serviceMonitor.enabled=true --set podMonitor.enabled=true
```

**A `PodMonitor` for planner and executor, a `ServiceMonitor` for ui-backend.**
Not an inconsistency — the executor holds `pods/eviction` and deliberately has
no Service, so that the component able to evict is unreachable over the
network. Giving it one so Prometheus could discover it would spend a security
property on a scrape target. A `PodMonitor` addresses pods directly and costs
nothing. `make lint` asserts no Service is ever rendered for either.

If `networkPolicy.enabled` and `serviceMonitor.enabled` are both on, the policy
admits `monitoringNamespace` (default `monitoring`). Without that the scrape is
dropped at the network layer and the target simply reads as down, with nothing
in the chart to explain why — so that is asserted too.

## What is published

Each component registers only the series it actually writes. The planner does
not publish eviction metrics: a permanent zero would read as "evictions are
fast" when the truth is that the process has never performed one. A missing
series is a question; a series pinned at zero is a wrong answer.

**Planner** — `dencer_plan_age_seconds`, `dencer_plan_steps{impact}`,
`dencer_plan_nodes_reclaimable`, `dencer_snapshot_nodes`,
`dencer_snapshot_pods`, `dencer_plan_cycle_seconds`,
`dencer_snapshot_failures_total`, `dencer_nodes_awaiting_reclamation`,
`dencer_reclamation_seconds`, `dencer_nodes_returned_total`

The reclamation series are the only ones that describe an outcome rather than a
plan. **`dencer_nodes_awaiting_reclamation` is the one to alert on**: it counts
nodes this product told someone to drain whose machine is still there. Rising
without falling means nothing is reclaiming them, and the capacity is
unavailable and still being paid for.

**Executor** — `dencer_runs_total{status}`, `dencer_guard_refusals_total{rule}`,
`dencer_eviction_duration_seconds`, `dencer_evictions_total{outcome}`,
`dencer_nodes_drained_total`, `dencer_recovery_wait_seconds`

**ui-backend** — Go runtime and process series only. It holds the SQLite writer,
so heap and file descriptors are what an operator would look at; request
counters are not there because no one has asked a question that needs them.

Three properties are worth calling out, because each is a way this could have
been quietly useless:

- **Plan age is computed when scraped, not written by the planning loop.** A
  gauge the loop sets would freeze at its last value if the loop died —
  reporting a fresh plan at exactly the moment there is none. It reads `-1`
  before the first plan, so a startup gap is distinguishable from a stall.
- **Every impact rating is set explicitly, including the zeroes.** An unset
  label vanishes from the scrape, and a missing series graphs as a gap rather
  than as "no Red steps".
- **A test parses the `Metrics` struct and fails on any field nothing writes.**
  A metric with no writer scrapes as zero, and zero is indistinguishable from
  healthy.

---

---

[← Documentation index](README.md) · [Project README](../README.md)
