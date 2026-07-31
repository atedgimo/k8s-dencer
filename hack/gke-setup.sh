#!/usr/bin/env bash
#
# One-time GCP project bootstrap for `make cloud-e2e`.
#
# Idempotent — safe to re-run. It enables the two APIs the cluster needs,
# checks the quota that most often makes a first run fail for a reason the
# error message does not explain, and offers a budget alert.
#
# What the cloud run costs, and why this script talks about money at all: a GKE
# cluster is the only thing in this repository that can bill you. Everything
# else is a container on your laptop. That asymmetry deserves a script that is
# explicit about it rather than a line in a README nobody reads twice.
#
#   ./hack/gke-setup.sh          check and fix
#   ./hack/gke-setup.sh check    report only, change nothing
#
set -euo pipefail

ZONE="${GCP_ZONE:-us-central1-a}"
MACHINE="${GCP_MACHINE:-e2-medium}"
NODES="${NODES:-5}"
BUDGET="${BUDGET:-1}"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
warn()  { printf '\033[33m%s\033[0m\n' "$*"; }
fail()  { red "FAIL: $*"; exit 1; }

CHECK_ONLY=0
[[ "${1:-}" == "check" ]] && CHECK_ONLY=1

command -v gcloud >/dev/null || fail "gcloud is required: https://cloud.google.com/sdk/docs/install"

bold "==> account and project"
account="$(gcloud config get-value account 2>/dev/null || true)"
[[ -n "$account" && "$account" != "(unset)" ]] || fail "not logged in. Run: gcloud auth login"
project="$(gcloud config get-value project 2>/dev/null || true)"
[[ -n "$project" && "$project" != "(unset)" ]] \
  || fail "no project set. Create one in the console, then: gcloud config set project <id>"
green "  ${account} on project ${project}"

bold "==> billing"
# GKE requires a billing account even when the free-tier credit covers the
# cluster fee. On the $300 trial this is the trial account, and it does not
# auto-charge when the credit runs out — GCP pauses resources and asks.
billing="$(gcloud billing projects describe "$project" \
  --format='value(billingEnabled)' 2>/dev/null || echo "unknown")"
if [[ "$billing" != "True" ]]; then
  fail "billing is not enabled on ${project}.
  GKE needs a billing account linked even on the free tier. Link the \$300 trial
  account at https://console.cloud.google.com/billing/linkedaccount?project=${project}"
fi
green "  enabled"

bold "==> APIs"
for api in container.googleapis.com compute.googleapis.com; do
  if gcloud services list --enabled --filter="config.name=${api}" \
       --format='value(config.name)' 2>/dev/null | grep -q "$api"; then
    green "  ${api} already enabled"
  elif [[ "$CHECK_ONLY" == "1" ]]; then
    warn "  ${api} NOT enabled"
  else
    echo "  enabling ${api} (this takes a minute)…"
    gcloud services enable "$api" --quiet
    green "  ${api} enabled"
  fi
done

bold "==> quota for ${NODES} × ${MACHINE} Spot in ${ZONE}"
# The trap this check exists for: a brand-new project can have a Spot/preemptible
# CPU quota of zero, and the cluster create then fails several minutes in with a
# message about "PREEMPTIBLE_CPUS" that reads like a bug in the script.
region="${ZONE%-*}"
needed=$(( NODES * 2 ))   # e2-medium is 2 vCPU

# Read the quotas out of JSON rather than a --format filter expression. The
# filter form returned nothing here and the check reported "could not read",
# which is how a limit of zero got waved through into a cluster create that
# would have failed several minutes later.
quota_json="$(gcloud compute regions describe "$region" --format=json 2>/dev/null || echo '{}')"
read -r SPOT_LIMIT ONDEMAND_LIMIT <<<"$(printf '%s' "$quota_json" | python3 -c '
import json, sys
try:
    q = {x["metric"]: x["limit"] for x in json.load(sys.stdin).get("quotas", [])}
except Exception:
    q = {}
print(int(q.get("PREEMPTIBLE_CPUS", -1)), int(q.get("CPUS", -1)))
')"

if [[ "${SPOT_LIMIT:--1}" -ge "$needed" ]]; then
  green "  ${SPOT_LIMIT} preemptible vCPUs available, ${needed} needed — Spot will be used"
elif [[ "${ONDEMAND_LIMIT:--1}" -ge "$needed" ]]; then
  # The common case on a new project: Spot quota starts at zero and has to be
  # requested. On-demand for twenty-five minutes is a few cents, so falling
  # back beats blocking on a quota request.
  warn "  preemptible vCPU quota in ${region} is ${SPOT_LIMIT}, and ${needed} are needed."
  warn "  On-demand quota is ${ONDEMAND_LIMIT}, so the run will use on-demand nodes instead."
  warn "  That is roughly 7 cents for a 25 minute run rather than 2. To use Spot,"
  warn "  request PREEMPTIBLE_CPUS at https://console.cloud.google.com/iam-admin/quotas"
else
  fail "neither preemptible (${SPOT_LIMIT}) nor on-demand (${ONDEMAND_LIMIT}) vCPU quota in
  ${region} covers the ${needed} vCPUs this needs. Request more, or run smaller:
    AGENTS=2 GCP_MACHINE=e2-small make cloud-e2e"
fi

bold "==> budget alert"
# Every gcloud call below runs with stdin closed. The Billing Budgets API is
# often not enabled on a new project, and gcloud responds by prompting to
# enable it — which, in a non-interactive script, is an indefinite hang rather
# than an error. This section is a nicety; it must never block the run.
exec 3<&0
# Cheap insurance against the one real failure mode: a cluster left running.
ba="$(gcloud billing projects describe "$project" \
  --format='value(billingAccountName)' </dev/null 2>/dev/null || true)"
if [[ -z "$ba" ]]; then
  warn "  could not read the billing account; skipping"
elif gcloud billing budgets list --billing-account="${ba##*/}" \
       --format='value(displayName)' </dev/null 2>/dev/null | grep -q "dencer-e2e"; then
  green "  already set"
elif [[ "$CHECK_ONLY" == "1" ]]; then
  warn "  no dencer-e2e budget alert"
else
  if gcloud billing budgets create --billing-account="${ba##*/}" </dev/null \
       --display-name="dencer-e2e" \
       --budget-amount="${BUDGET}USD" \
       --threshold-rule=percent=0.5 --threshold-rule=percent=1.0 \
       --quiet >/dev/null 2>&1; then
    green "  \$${BUDGET} budget alert created"
  else
    # Needs the Billing Budgets API and billing.budgets.create, which a trial
    # account may not grant. Not worth failing setup over.
    warn "  could not create one (needs billing.budgets.create). Set a budget by hand:"
    warn "  https://console.cloud.google.com/billing/budgets"
  fi
fi

echo
bold "What a run costs"
cat <<EOF
  control plane   free      GKE's free tier credit covers one zonal cluster
  ${NODES} × ${MACHINE} Spot   ~\$0.04/hr
  ${NODES} × 20GB pd-standard ~\$0.01/hr
  ---------------------------------------------------------------
  a ~25 minute run    roughly two to three cents

  The cluster is destroyed on every exit path, including Ctrl-C. The one way
  this costs real money is a cluster left running: four idle nodes is about
  \$100/month. If a run is ever interrupted so hard the trap does not fire:

    ./hack/e2e.sh clean            # or: make cloud-e2e-clean
    gcloud container clusters list # should be empty
EOF

echo
green "ready:  make cloud-e2e"
