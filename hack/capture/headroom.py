#!/usr/bin/env python3
"""Can this cluster actually demonstrate a drain?

The 2026-08-15 GKE run answered its P0 — the product plans on a real managed
cluster — and then never executed a single drain, because the cluster was
88.5% requested for most of the window and there was nowhere to move pods to.
Nobody knew that until minute forty, by which point the cluster was most of
the way through its life.

This answers it at minute three, from the live cluster rather than from a
formula. A formula was tried on paper first and was wrong: the obvious guess
is that a managed node's ~470m of system requests is fixed per-node overhead,
and it is not — most of it is Deployments (kube-dns alone is 270m) that move
like any other pod. Only the DaemonSets and the node-owned static pods are
genuinely per-node, and on the measured cluster that was ~276m, not ~470m.
Guessing wrong in that direction says a cluster is hopeless when it is fine.

So: read what is actually there, split it the way the planner splits it, and
say how many nodes could come out.

This is a CAPACITY CEILING, not a plan. It answers "is there physically room
to put these pods somewhere else", and ignores every constraint that might
refuse the move — PDBs, topology spread, anti-affinity, taints. Constraints
can only take nodes off this number, never add them. So a zero here is
decisive (the run cannot demonstrate a drain and the fleet needs to be
bigger) and a healthy number is necessary rather than sufficient.

Reads `kubectl get nodes -o json` and `kubectl get pods -A -o json`, in that
order, from two files named on the command line.

    kubectl get nodes -o json > /tmp/n.json
    kubectl get pods -A -o json > /tmp/p.json
    headroom.py /tmp/n.json /tmp/p.json [PACK_CEILING]
"""
import json
import sys
from collections import defaultdict


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


def pinned(pod):
    """Can this pod never move off its node?

    The same two cases the planner treats as pinned rather than blocking:
    a DaemonSet puts one on every node by definition, and a static pod is
    owned by the kubelet that runs it. Both survive their node going away
    at no cost, so neither counts as work that has to be re-homed — and
    both consume room on every node that stays.
    """
    for ref in pod["metadata"].get("ownerReferences") or []:
        if ref.get("kind") == "DaemonSet":
            return True
    ann = pod["metadata"].get("annotations") or {}
    return "kubernetes.io/config.mirror" in ann


nodes_json, pods_json = sys.argv[1], sys.argv[2]
ceiling = float(sys.argv[3]) if len(sys.argv) > 3 else 0.85

nodes = {}
for n in json.load(open(nodes_json))["items"]:
    name = n["metadata"]["name"]
    unsched = (n.get("spec") or {}).get("unschedulable", False)
    nodes[name] = {
        "alloc": cpu_m((n.get("status") or {}).get("allocatable", {}).get("cpu")),
        "cordoned": bool(unsched),
    }

pinned_by_node = defaultdict(int)
movable_total = 0
movable_by_node = defaultdict(int)

for p in json.load(open(pods_json))["items"]:
    node = (p.get("spec") or {}).get("nodeName")
    if not node or node not in nodes:
        continue
    if (p.get("status") or {}).get("phase") in ("Succeeded", "Failed"):
        continue
    c = sum(
        cpu_m(((ct.get("resources") or {}).get("requests") or {}).get("cpu"))
        for ct in p["spec"].get("containers", [])
    )
    if pinned(p):
        pinned_by_node[node] += c
    else:
        movable_total += c
        movable_by_node[node] += c

live = [n for n, d in nodes.items() if not d["cordoned"]]
if not live:
    print("no schedulable nodes")
    sys.exit(1)

print(f"{'node':<48}{'alloc':>9}{'pinned':>9}{'movable':>10}{'room':>9}")
for n in sorted(live):
    d = nodes[n]
    room = int((d["alloc"] - pinned_by_node[n]) * ceiling)
    print(f"{n:<48}{str(d['alloc']) + 'm':>9}{str(pinned_by_node[n]) + 'm':>9}"
          f"{str(movable_by_node[n]) + 'm':>10}{str(room) + 'm':>9}")

# Room per node is allocatable minus what can never leave it, times the
# ceiling the planner will not pack above. Sorted descending and consumed
# greedily: the planner is first-fit-decreasing, so the best case is the
# emptiest-of-pinned nodes absorbing everything.
rooms = sorted(
    (int((nodes[n]["alloc"] - pinned_by_node[n]) * ceiling) for n in live),
    reverse=True,
)
need, acc = 0, 0
for r in rooms:
    if acc >= movable_total:
        break
    acc += r
    need += 1

drainable = len(live) - need if acc >= movable_total else 0

print()
print(f"  schedulable nodes    {len(live)}")
print(f"  movable CPU          {movable_total}m")
print(f"  usable room          {sum(rooms)}m  (allocatable minus pinned, x{ceiling} ceiling)")
print(f"  nodes it needs       {need}")
print()

# The verdict, in the terms the run cares about. Two is the threshold rather
# than one because a single drainable node gives one shot: if that step is
# rated Red, or its target is where the executor happens to be sitting, the
# run has nothing else to offer and the trip is wasted.
print("  A capacity ceiling, not a plan: constraints (PDBs, spread, affinity)")
print("  can only take nodes off this number. Zero here is decisive.")
print()

if drainable >= 2:
    print(f"  OK — capacity for {drainable} drains. Enough to execute one and still have a spare.")
elif drainable == 1:
    print("  THIN — exactly 1 node is drainable. If that step is rated Red there is")
    print("  no second option, and the run demonstrates nothing.")
    print("  Add nodes (REAL_NODES) rather than shrinking the demo: the movable load")
    print("  is what makes the plan interesting.")
else:
    print("  TOO PACKED — nothing can be drained. This is the 2026-08-15 run repeating:")
    print(f"  {movable_total}m of movable work needs every one of {len(live)} nodes.")
    print("  Relaunch with more nodes. The demo workload is the point; the fleet is not.")

sys.exit(0 if drainable >= 1 else 2)
