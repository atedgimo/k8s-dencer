# Security policy

k8s-dencer can cordon nodes and evict pods. A vulnerability here does not leak
data so much as move production workloads, so please report privately rather
than opening an issue.

## Reporting a vulnerability

Use **[GitHub private vulnerability reporting](https://github.com/atedgimo/k8s-dencer/security/advisories/new)**.
It is enabled on this repository and goes only to the maintainers.

If that is unavailable to you, email **atedgimo@gmail.com** with `k8s-dencer
security` in the subject.

Please include the version or commit, the chart values that matter (especially
whether `executor.enabled` and `auth.enabled` are on), and what an attacker
would gain. A proof of concept is welcome but not required.

**What to expect:** an acknowledgement within 3 working days, an assessment
within 10, and credit in the advisory unless you would rather not be named.
This is a small project maintained by one person — those are honest targets,
not a contractual SLA.

Please give a reasonable window to ship a fix before disclosing publicly.

## Supported versions

Only the latest release. There are no maintenance branches; fixes land on
`main` and go out in the next tag.

## What counts

**In scope**

- Anything that lets a caller cordon or evict without holding
  `create consolidations.dencer.io`, or read plans without
  `get plans.dencer.io`.
- Any path that reaches the eviction API while bypassing the Safety Guard's
  rails, or that executes a Red step with no open `MaintenanceWindow`.
- Privilege escalation out of any of the four workloads — in particular
  anything that lets the network-reachable `ui-backend` perform an action only
  the `executor` should.
- Authentication or authorization bypass in the API, the CLI, or the MCP
  surface exposed to Kagent.
- Anything in the chart that grants more than the documented RBAC, or that
  makes the executor reachable over the network.

**Out of scope**

- Findings against a deployment with `auth.enabled=false`. That configuration
  is unauthenticated by definition, the chart refuses to enable the executor
  alongside it, and `make lint` fails the build if any shipped profile turns it
  off.
- The KWOK demo fabric and `demo/`. They exist to fake nodes on a laptop and
  are not for production.
- Credentials in `hack/`. Those IdPs are throwaway containers that live for the
  length of one script, and their passwords are generated per run.
- Denial of service through legitimately expensive requests. The measured
  ceiling is documented in [docs/benchmarks.md](docs/benchmarks.md).
- Anything requiring cluster-admin to set up. An attacker who is already
  cluster-admin does not need this tool.

## The security model, briefly

Worth knowing before reporting, because several apparent problems are
deliberate:

- **There is no credential store.** Identity is verified by `TokenReview` and
  permissions by `SubjectAccessReview`, both answered by the Kubernetes API
  server. k8s-dencer holds no passwords, no sessions and no user database.
- **Eviction, never deletion.** Disruption goes through the `policy/v1`
  eviction subresource, so PodDisruptionBudgets are enforced by the API server
  rather than by code here. The executor is deliberately *not* granted
  `delete pods`.
- **The component reachable over the network cannot evict.** `ui-backend` has a
  Service and no write verbs; `executor` can evict and has no Service. Chart
  lint assertions hold both halves, including one that fails if scraping ever
  gives the executor a Service.
- **Execution is off by default**, and the chart refuses `executor.enabled=true`
  without authentication and persistence.

Full detail in [docs/security.md](docs/security.md) and
[docs/execution.md](docs/execution.md), including instructions for verifying
the privilege split against your own cluster rather than taking these claims on
trust.
