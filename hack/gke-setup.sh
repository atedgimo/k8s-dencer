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
spot_quota="$(gcloud compute regions describe "$region" \
  --format='value(quotas.filter("metric:PREEMPTIBLE_CPUS").limit)' 2>/dev/null || echo "")"
needed=$(( NODES * 2 ))   # e2-medium is 2 vCPU
if [[ -z "$spot_quota" ]]; then
  warn "  could not read PREEMPTIBLE_CPUS quota for ${region}; the run may still work"
elif (( ${spot_quota%.*} < needed )); then
  warn "  PREEMPTIBLE_CPUS in ${region} is ${spot_quota%.*}, and ${NODES} × ${MACHINE} needs ${needed}."
  warn "  Request more at https://console.cloud.google.com/iam-admin/quotas, or run with"
  warn "  fewer/smaller nodes:  AGENTS=2 GCP_MACHINE=e2-small make cloud-e2e"
else
  green "  ${spot_quota%.*} preemptible vCPUs available, ${needed} needed"
fi

bold "==> budget alert"
# Cheap insurance against the one real failure mode: a cluster left running.
ba="$(gcloud billing projects describe "$project" \
  --format='value(billingAccountName)' 2>/dev/null || true)"
if [[ -z "$ba" ]]; then
  warn "  could not read the billing account; skipping"
elif gcloud billing budgets list --billing-account="${ba##*/}" \
       --format='value(displayName)' 2>/dev/null | grep -q "dencer-e2e"; then
  green "  already set"
elif [[ "$CHECK_ONLY" == "1" ]]; then
  warn "  no dencer-e2e budget alert"
else
  if gcloud billing budgets create --billing-account="${ba##*/}" \
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
