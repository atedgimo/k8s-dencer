# Documentation

| | |
|---|---|
| **[Installation and configuration](install.md)** | Installing the chart, values profiles, and the constraints each choice carries |
| **[CLI](cli.md)** | `dencer` and `kubectl dencer` — inspect and run plans without the UI |
| **[Architecture](architecture.md)** | Components, the constraint analyzer, the planner, impact ratings, the API and the UI |
| **[Authentication and authorization](security.md)** | TokenReview and SubjectAccessReview delegation, OIDC/SSO, and how to verify the privilege split yourself |
| **[Execution and safety](execution.md)** | What the executor does per step, the Safety Guard's rails, maintenance windows, and the audit trail |
| **[Observability](observability.md)** | Prometheus metrics and what each component publishes |
| **[Running the cloud test on GCP](gcp-setup.md)** | Account, project, billing, quota — everything to do by hand, once |
| **[Development](development.md)** | The local loop, the KWOK fake-node fabric, the CI gates, and cutting a release |
| **[Benchmarks](benchmarks.md)** | Measured cost per operation, and where each stage stops being usable |
| **[Findings](findings.md)** | Bugs and gaps found by running it — what hid, and why |
| **[Roadmap and status](roadmap.md)** | What is built, what is planned, and what was dropped |
| **[Security policy](../SECURITY.md)** | Reporting a vulnerability, and what is in scope |
| **[Design document](k8s-consolidation-agent-architecture.md)** | The original architecture and the reasoning behind it |

---

[← Project README](../README.md)
