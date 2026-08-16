# Documentation

Grouped by what you are trying to do, because a flat list of fifteen files
tells you nothing about which one to open first.

## Running it

Start here if you want it on a cluster.

| | |
|---|---|
| **[Installation and configuration](install.md)** | Installing the chart, values profiles, and the constraints each choice carries |
| **[CLI](cli.md)** | `dencer` and `kubectl dencer` — inspect and run plans without the UI |
| **[Execution and safety](execution.md)** | What the executor does per step, the Safety Guard's rails, maintenance windows, and the audit trail |
| **[Authentication and authorization](security.md)** | TokenReview and SubjectAccessReview delegation, OIDC/SSO, and how to verify the privilege split yourself |
| **[Observability](observability.md)** | Prometheus metrics and what each component publishes |

## Understanding it

How the thing is built, and why it decides what it decides.

| | |
|---|---|
| **[Architecture](architecture.md)** | Components, the constraint analyzer, the planner, impact ratings, the API and the UI |
| **[Benchmarks](benchmarks.md)** | Measured cost per operation, and where each stage stops being usable |

## Working on it

| | |
|---|---|
| **[Development](development.md)** | The local loop, the KWOK fake-node fabric, the CI gates, and cutting a release |
| **[Running the cloud test on GCP](gcp-setup.md)** | Account, project, billing, quota — everything to do by hand, once |
| **[The GCP playground](gcp-playground.md)** | A timed, self-destructing GKE cluster with real workloads and a random scenario |

## What happened, and what is next

Four views of the same history, kept apart because they answer different
questions.

| | |
|---|---|
| **[Release history](releases.md)** | What changed in each release, and whether you should hurry |
| **[Product roadmap](product-roadmap.md)** | Shipped capabilities and what comes next, as features rather than milestones |
| **[Engineering roadmap](roadmap.md)** | The milestone history — measurements, design reasoning, what was dropped and why |
| **[Findings](findings.md)** | Bugs and gaps found by running it, grouped by *how they hid* rather than by when |

## Reference

| | |
|---|---|
| **[Security policy](../SECURITY.md)** | Reporting a vulnerability, and what is in scope |
| **[Original design document](k8s-consolidation-agent-architecture.md)** | Historical: what was intended, before the code disagreed. See [architecture.md](architecture.md) for what actually runs |

---

[← Project README](../README.md)
