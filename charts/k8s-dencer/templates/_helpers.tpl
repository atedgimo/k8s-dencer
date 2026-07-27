{{/*
Chart name, overridable.
*/}}
{{- define "k8s-dencer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified release name, capped at 63 chars so component suffixes and
generated label values stay valid.
*/}}
{{- define "k8s-dencer.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 50 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 50 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "k8s-dencer.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Labels shared by every object in the release.
*/}}
{{- define "k8s-dencer.labels" -}}
helm.sh/chart: {{ include "k8s-dencer.chart" . }}
{{ include "k8s-dencer.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: k8s-dencer
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{- define "k8s-dencer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s-dencer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Per-component variants. Call with (dict "ctx" $ "component" "planner").
Selector labels must never include chart version, or an upgrade would try to
mutate the immutable Deployment selector.
*/}}
{{- define "k8s-dencer.componentSelectorLabels" -}}
{{ include "k8s-dencer.selectorLabels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "k8s-dencer.componentLabels" -}}
{{ include "k8s-dencer.labels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "k8s-dencer.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "k8s-dencer.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Per-component ServiceAccount name. Call with (dict "component" "planner" "ctx" $).

Each component runs under its own account so permissions can differ: only
ui-backend may create TokenReviews and SubjectAccessReviews, and from Phase 2
only the executor may evict. A shared account would mean the widest permission
any component needs is held by all of them, which defeats the point of the
executor having its own in the first place.

Setting serviceAccount.name overrides this and runs every component under one
existing account — an escape hatch for clusters that pre-provision identities
(IRSA, Workload Identity), at the cost of that separation.
*/}}
{{- define "k8s-dencer.componentServiceAccountName" -}}
{{- $ctx := .ctx -}}
{{- if $ctx.Values.serviceAccount.name -}}
{{- $ctx.Values.serviceAccount.name -}}
{{- else if $ctx.Values.serviceAccount.create -}}
{{- printf "%s-%s" (include "k8s-dencer.fullname" $ctx) .component -}}
{{- else -}}
default
{{- end -}}
{{- end }}

{{/*
Image reference. Call with (dict "image" .Values.<component>.image "ctx" $).
Tag falls back to the chart appVersion so a chart release pins a coherent set.
*/}}
{{- define "k8s-dencer.image" -}}
{{- $tag := .image.tag | default .ctx.Chart.AppVersion -}}
{{- if .image.registry -}}
{{- printf "%s/%s:%s" .image.registry .image.repository $tag -}}
{{- else -}}
{{- printf "%s:%s" .image.repository $tag -}}
{{- end -}}
{{- end }}

{{/*
Name of the PVC backing the plan store.
*/}}
{{- define "k8s-dencer.pvcName" -}}
{{- if .Values.persistence.existingClaim -}}
{{- .Values.persistence.existingClaim -}}
{{- else -}}
{{- printf "%s-data" (include "k8s-dencer.fullname" .) -}}
{{- end -}}
{{- end }}

{{/*
Namespace hosting the Kagent resources.
*/}}
{{- define "k8s-dencer.kagentNamespace" -}}
{{- default .Release.Namespace .Values.kagent.namespace -}}
{{- end }}

{{/*
In-cluster URL of the ui-backend, used by the Kagent RemoteMCPServer. Fully
qualified because the agent may live in a different namespace.
*/}}
{{- define "k8s-dencer.uiBackendURL" -}}
{{- printf "http://%s-ui-backend.%s.svc:%v" (include "k8s-dencer.fullname" .) .Release.Namespace .Values.uiBackend.service.port -}}
{{- end }}
