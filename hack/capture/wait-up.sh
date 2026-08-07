#!/usr/bin/env bash
# Wait for the playground cluster, wire up a named context, restore the
# user's default context, then wait for the product to be Ready.
set -uo pipefail
ZONE=us-central1-a; NAME=dencer-play
PREV="$(kubectl config current-context 2>/dev/null || echo orbstack)"

echo "waiting for GKE cluster $NAME to reach RUNNING ..."
until [ "$(gcloud container clusters list --filter="name=$NAME" --format='value(status)' 2>/dev/null)" = "RUNNING" ]; do
  sleep 20
done
echo "cluster RUNNING"

PROJ="$(gcloud config get-value project 2>/dev/null)"
gcloud container clusters get-credentials "$NAME" --zone "$ZONE" >/dev/null 2>&1
kubectl config rename-context "gke_${PROJ}_${ZONE}_${NAME}" gke-play >/dev/null 2>&1
# get-credentials hijacks the current context; hand it straight back.
kubectl config use-context "$PREV" >/dev/null 2>&1
echo "context gke-play ready; default context restored to $PREV"

echo "waiting for nodes ..."
until [ "$(kubectl --context gke-play get nodes --no-headers 2>/dev/null | grep -c ' Ready')" -ge 1 ]; do sleep 15; done
kubectl --context gke-play get nodes --no-headers 2>/dev/null | wc -l | xargs echo "nodes present:"

echo "waiting for the product to be Ready (up to ~10m) ..."
kubectl --context gke-play -n k8s-dencer wait --for=condition=available deploy --all --timeout=600s 2>&1 | tail -4
kubectl --context gke-play -n k8s-dencer get pods 2>&1
echo "=== READY ==="
