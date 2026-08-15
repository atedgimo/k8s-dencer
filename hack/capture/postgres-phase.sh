#!/usr/bin/env bash
#
# The Postgres phase of a GKE playground run.
#
# Deliberately a second step rather than a flag on the launch. The run exists
# to answer one question — does this version plan on a real managed cluster —
# and CHECKLIST item 1 says stop if it fails. Installing against Postgres from
# the start would put a database pod on a node whose 940m allocatable is
# already 30-80% spoken for by GKE's own daemons, changing what the planner
# sees on the exact run meant to measure that. So: SQLite answers the P0
# question, then this converts the same cluster and asks it again.
#
# Run it after checklist items 1-3 have passed, with time left in the window.
#
#   ./hack/capture/postgres-phase.sh
#
# What it costs: a Postgres pod on nodes already paid for, plus one 10Gi PD
# for the rest of the window. Fractions of a cent. The expense of this run is
# the six e2-medium nodes, and they are already running.
#
# The PD is not optional. This exercise drains nodes for a living, and the
# database is an ordinary workload that can be evicted by the drain it is
# recording. On a claim it comes back with its data; on an emptyDir it comes
# back empty and the ui-backend then serves "relation does not exist" against
# a schema it migrated minutes earlier. That is not a hypothetical — it
# happened three times in the e2e this week before the cause was understood.

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# The same chart the playground installed, so this cannot drift to a different
# version mid-exercise.
REPO_CHART="${REPO_CHART:-$REPO/charts/k8s-dencer}"

CTX="${CTX:-$(kubectl config current-context)}"
NS="${NS:-k8s-dencer}"
RELEASE="${RELEASE:-k8s-dencer}"
# Reuse the password already in the cluster if there is one.
#
# POSTGRES_PASSWORD only takes effect when the data directory is initialised.
# The claim outlives the pod, so a second run that minted a fresh password
# would leave the Secret and the database disagreeing, and every component
# would crashloop on "password authentication failed" — which is what
# happened the first time this was rehearsed. Idempotence here is not
# tidiness; it is the difference between re-running this during a metered
# window and losing the window.
EXISTING="$(kubectl --context "$CTX" -n "$NS" \
  get secret play-db -o jsonpath='{.data.password}' 2>/dev/null || true)"
if [[ -n "$EXISTING" ]]; then
  PG_PASSWORD="$(printf '%s' "$EXISTING" | base64 --decode)"
else
  PG_PASSWORD="${PG_PASSWORD:-$(head -c 18 /dev/urandom | base64 | tr -d '/+=' )}"
fi
# standard-rwo is GKE's. Overridable so this can be rehearsed on a local
# cluster before it is run against something that costs money.
PG_STORAGE_CLASS="${PG_STORAGE_CLASS:-standard-rwo}"
PG_PF_PORT="${PG_PF_PORT:-18808}"

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
fail()  { red "ERROR: $*"; exit 1; }

kubectl --context "$CTX" -n "$NS" get deploy "${RELEASE}-ui-backend" >/dev/null 2>&1 \
  || fail "no k8s-dencer release in ${NS} on context ${CTX}; run the playground first"

bold "==> the ledger before the switch"
# Recorded, because it is about to be left behind. There is no migration path
# from the SQLite store (#181, closed deliberately): the plan is recomputed
# within a resync, but the reclamation ledger and audit trail stay in the file.
# Through a port-forward rather than kubectl exec: the component images are
# distroless and have no shell and no wget in them, which is the point of
# distroless and was discovered the hard way.
kubectl --context "$CTX" -n "$NS" port-forward "svc/${RELEASE}-ui-backend" "${PG_PF_PORT}:8080" >/dev/null 2>&1 &
PF=$!
trap 'kill $PF 2>/dev/null || true' EXIT
sleep 4
if TOKEN="$(kubectl --context "$CTX" -n "$NS" create token "${RELEASE}-ui-backend" --duration=10m 2>/dev/null)"; then
  curl -s --max-time 8 -H "Authorization: Bearer ${TOKEN}" \
    "http://localhost:${PG_PF_PORT}/api/v1/reclamations" | head -c 400 || true
  echo
fi
kill $PF 2>/dev/null || true
trap - EXIT

bold "==> postgres, on a claim"
kubectl --context "$CTX" -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Secret
metadata:
  name: play-db
type: Opaque
stringData:
  password: ${PG_PASSWORD}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: play-postgres
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: ${PG_STORAGE_CLASS}
  resources:
    requests:
      storage: 10Gi
---
apiVersion: v1
kind: Service
metadata:
  name: play-postgres
spec:
  selector: {app: play-postgres}
  ports: [{port: 5432, targetPort: 5432}]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: play-postgres
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: play-postgres}}
  template:
    metadata:
      labels: {app: play-postgres}
    spec:
      # fsGroup, or nothing starts on a real cloud disk.
      #
      # A GKE PersistentDisk mounts owned by root. Postgres runs as uid 70 and
      # cannot create its own data directory:
      #
      #   mkdir: can't create directory '/var/lib/postgresql/data/pgdata':
      #   Permission denied
      #
      # fsGroup makes the kubelet chown the volume on mount. This passed every
      # rehearsal on OrbStack's local-path and on emptyDir, both permissive,
      # and failed the moment it met real storage — which is the entire
      # argument for having run this phase on GKE at all.
      securityContext:
        runAsNonRoot: true
        runAsUser: 70
        fsGroup: 70
      containers:
        - name: postgres
          image: postgres:16-alpine
          env:
            - {name: POSTGRES_DB, value: dencer}
            - {name: POSTGRES_USER, value: dencer}
            - {name: POSTGRES_PASSWORD, valueFrom: {secretKeyRef: {name: play-db, key: password}}}
            - {name: PGDATA, value: /var/lib/postgresql/data/pgdata}
          ports: [{containerPort: 5432}]
          readinessProbe:
            exec: {command: ["pg_isready","-U","dencer"]}
            periodSeconds: 3
          resources:
            requests: {cpu: 100m, memory: 256Mi}
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            runAsUser: 70
            capabilities: {drop: ["ALL"]}
            seccompProfile: {type: RuntimeDefault}
          volumeMounts: [{name: data, mountPath: /var/lib/postgresql/data}]
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: play-postgres}
YAML

kubectl --context "$CTX" -n "$NS" rollout status deploy/play-postgres --timeout=300s >/dev/null \
  || fail "postgres did not become ready"
green "  ready, on a 10Gi ${PG_STORAGE_CLASS} claim"

bold "==> converting the release"
# replicaCount 2 is the point of the exercise: it is the constraint SQLite
# imposes and Postgres lifts, and it also puts two migrators in the race the
# advisory lock exists for. persistence off is correct here, not a shortcut —
# the chart gates the claim and the /data mount away on this backend.
helm --kube-context "$CTX" upgrade "$RELEASE" "$REPO_CHART" \
  --namespace "$NS" --reuse-values \
  --set database.type=postgres \
  --set database.postgres.host=play-postgres \
  --set database.postgres.database=dencer \
  --set database.postgres.user=dencer \
  --set database.postgres.existingSecret=play-db \
  --set database.postgres.sslMode=disable \
  --set persistence.enabled=false \
  --set uiBackend.replicaCount=2 \
  --wait --timeout 420s >/dev/null || {
    kubectl --context "$CTX" -n "$NS" get pods -o wide
    kubectl --context "$CTX" -n "$NS" get events --sort-by=.lastTimestamp | tail -20
    fail "the release did not come up on postgres"
  }
green "  converted"

bold "==> what to check now"
cat <<'NOTES'
  1. Every pod Running with 0 restarts. Two ui-backends and a planner all
     migrate at once; before the advisory lock one died on a duplicate key
     and CrashLoopBackOffed until the other committed.
  2. No PersistentVolumeClaim for the plan store, and no /data mount. If
     either survived, the single-node constraint came with it.
  3. A plan appears, with steps. This is CHECKLIST item 1 asked a second
     time, on the other backend.
  4. Drain one step and confirm it reaches the ledger. That is exactly what
     v0.6.0 got wrong: the drain succeeded, the executor's events looked
     perfect, and nothing was written to the store.

  The ledger printed at the top of this run is gone — there is no migration
  path from the file, by decision, and the reclamation history stays in it.

  The old k8s-dencer-data claim is still there, and that is correct: the
  chart marks it helm.sh/resource-policy: keep so changing backends never
  destroys an operator's plan history. It costs a Persistent Disk until the
  cluster goes away. Deleting the cluster removes it; hack/capture/
  verify-teardown.sh is what confirms nothing was left behind.
NOTES

kubectl --context "$CTX" -n "$NS" get pods -o wide
