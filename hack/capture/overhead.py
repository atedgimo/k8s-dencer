#!/usr/bin/env python3
"""Sum pod CPU/memory *requests* per node, split system vs workload.

This is the measurement that answers "how much does a managed node spend on
itself before your workload gets any" — the number we could not verify after
the last playground cluster was torn down.

Reads `kubectl get pods -A -o json` on stdin.
"""
import json
import sys
from collections import defaultdict

SYSTEM_NS = {
    "kube-system", "gke-managed-cim", "gke-managed-system", "gmp-system",
    "gmp-public", "gke-gmp-system", "kube-node-lease", "kube-public",
    "gke-managed-filestorecsi", "gke-managed-volumepopulator",
}


def cpu_m(v):
    """Kubernetes CPU quantity -> millicores."""
    if not v:
        return 0
    v = str(v)
    if v.endswith("m"):
        return int(float(v[:-1]))
    if v.endswith("n"):
        return int(float(v[:-1]) / 1_000_000)
    return int(float(v) * 1000)


def mem_mi(v):
    if not v:
        return 0
    v = str(v)
    units = {"Ki": 1 / 1024, "Mi": 1, "Gi": 1024, "Ti": 1024 * 1024,
             "K": 1000 / 1048576, "M": 1000000 / 1048576, "G": 1000000000 / 1048576}
    for suf, mult in units.items():
        if v.endswith(suf):
            return int(float(v[: -len(suf)]) * mult)
    return int(float(v) / 1048576)


pods = json.load(sys.stdin)["items"]
sysc = defaultdict(int)
wrkc = defaultdict(int)
sysm = defaultdict(int)
wrkm = defaultdict(int)
sys_pods = defaultdict(list)

for p in pods:
    node = (p.get("spec") or {}).get("nodeName")
    if not node or (p.get("status") or {}).get("phase") in ("Succeeded", "Failed"):
        continue
    ns = p["metadata"]["namespace"]
    c = sum(cpu_m(((ct.get("resources") or {}).get("requests") or {}).get("cpu"))
            for ct in p["spec"].get("containers", []))
    m = sum(mem_mi(((ct.get("resources") or {}).get("requests") or {}).get("memory"))
            for ct in p["spec"].get("containers", []))
    if ns in SYSTEM_NS:
        sysc[node] += c
        sysm[node] += m
        if c:
            sys_pods[p["metadata"]["name"].rsplit("-", 1)[0]] = [ns, c]
    else:
        wrkc[node] += c
        wrkm[node] += m

nodes = sorted(set(sysc) | set(wrkc))
print(f"{'node':<48}{'system cpu':>12}{'workload cpu':>14}{'system mem':>13}")
for n in nodes:
    print(f"{n:<48}{str(sysc[n]) + 'm':>12}{str(wrkc[n]) + 'm':>14}{str(sysm[n]) + 'Mi':>13}")

if nodes:
    tot = sum(sysc[n] for n in nodes)
    print(f"\nsystem CPU requests per node: min {min(sysc[n] for n in nodes)}m  "
          f"max {max(sysc[n] for n in nodes)}m  mean {tot // len(nodes)}m  "
          f"across {len(nodes)} nodes")

print("\nsystem pods requesting CPU (by controller):")
for name, (ns, c) in sorted(sys_pods.items(), key=lambda kv: -kv[1][1]):
    print(f"  {c:>5}m  {ns}/{name}")
