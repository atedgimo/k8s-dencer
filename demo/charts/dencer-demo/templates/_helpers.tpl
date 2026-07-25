{{- define "dencer-demo.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: dencer-demo
dencer.io/synthetic: "true"
{{- end }}

{{/*
Pins a workload to the KWOK fabric.

Both halves are required: the nodeAffinity keeps synthetic pods off the one
real node, and the toleration gets them past the taint that keeps real
workloads (kagent, k8s-dencer itself) off the fake ones.
*/}}
{{- define "dencer-demo.nodeAffinity" -}}
nodeAffinity:
  requiredDuringSchedulingIgnoredDuringExecution:
    nodeSelectorTerms:
      - matchExpressions:
          - key: type
            operator: In
            values:
              - kwok
{{- end }}

{{- define "dencer-demo.tolerations" -}}
tolerations:
  - key: kwok.x-k8s.io/node
    operator: Exists
    effect: NoSchedule
{{- end }}

{{/*
Standard container for a synthetic pod. Never executed — kwok-controller fakes
the lifecycle — so only the resource requests carry meaning. Those requests are
the entire input to the bin-packer, since this cluster has no metrics-server.

Call with (dict "ctx" $ "cpu" "1" "memory" "2Gi").
*/}}
{{- define "dencer-demo.container" -}}
- name: workload
  image: {{ .ctx.Values.image }}
  resources:
    requests:
      cpu: {{ .cpu | quote }}
      memory: {{ .memory }}
    limits:
      cpu: {{ .cpu | quote }}
      memory: {{ .memory }}
{{- end }}
