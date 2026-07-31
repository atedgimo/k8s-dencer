#!/usr/bin/env bash
# Read-only audit of billable GKE/GCE leftovers in the current project.
#
# The cloud test tears itself down, but "the script said so" is not the same
# as "nothing is billing". This lists every resource class a k8s-dencer cloud
# run could leave behind — clusters, disks, load balancers, static IPs — with
# zero mutations: every command here is a list. Run it after any cloud
# session; an empty report is the receipt.
set -euo pipefail

bold() { printf '\033[1m%s\033[0m\n' "$*"; }

PROJECT="$(gcloud config get-value project 2>/dev/null)"
[ -n "$PROJECT" ] || { echo "no gcloud project configured"; exit 1; }
bold "==> billable leftovers audit for project: $PROJECT (read-only)"

found=0
check() {
  local what="$1"; shift
  local out
  out="$("$@" 2>/dev/null || true)"
  if [ -n "$out" ]; then
    found=1
    printf '\033[33m▲ %s\033[0m\n%s\n\n' "$what" "$out"
  else
    printf '\033[32m  none: %s\033[0m\n' "$what"
  fi
}

check "GKE clusters"        gcloud container clusters list --format="table(name,location,currentNodeCount)" 2>/dev/null
check "Compute instances"   gcloud compute instances list --format="table(name,zone,status)"
check "Persistent disks"    gcloud compute disks list --format="table(name,zone,sizeGb,users.basename())"
check "Forwarding rules"    gcloud compute forwarding-rules list --format="table(name,region,IPAddress)"
check "Target pools"        gcloud compute target-pools list --format="table(name,region)"
check "Static addresses"    gcloud compute addresses list --filter="status=RESERVED" --format="table(name,region,address)"
check "Disk snapshots"      gcloud compute snapshots list --format="table(name,diskSizeGb)"

echo
if [ "$found" = 0 ]; then
  bold "Nothing billable found. This is the receipt."
else
  bold "Leftovers above are billing. Delete what you recognise; nothing here deletes anything."
fi
