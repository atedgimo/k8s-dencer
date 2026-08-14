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

{{/*
The plan store environment, shared by planner, executor and ui-backend.

One definition because the three components must agree about which database
they are talking to. They did not: ui-backend had a postgres branch, planner
had none, and executor set no DATABASE_TYPE at all — so selecting postgres
would have left the executor quietly opening a SQLite file nobody else was
writing to, with no error anywhere. A disagreement about the store is invisible
at deploy time and looks like lost plans at run time.

The password is never a value and never appears in the rendered manifest; it
is read from a Secret the operator already has.
*/}}
{{- define "k8s-dencer.databaseEnv" -}}
- name: DATABASE_TYPE
  value: {{ .Values.database.type | quote }}
{{- if eq .Values.database.type "sqlite" }}
- name: DATABASE_PATH
  value: {{ .Values.database.sqlite.path | quote }}
{{- else }}
- name: POSTGRES_HOST
  value: {{ .Values.database.postgres.host | quote }}
- name: POSTGRES_PORT
  value: {{ .Values.database.postgres.port | quote }}
- name: POSTGRES_DATABASE
  value: {{ .Values.database.postgres.database | quote }}
- name: POSTGRES_USER
  value: {{ .Values.database.postgres.user | quote }}
- name: POSTGRES_SSLMODE
  value: {{ .Values.database.postgres.sslMode | quote }}
{{- with .Values.database.postgres.existingSecret }}
- name: POSTGRES_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ . }}
      key: {{ $.Values.database.postgres.existingSecretPasswordKey }}
{{- end }}
{{- /*
  Nil-safe, because helm upgrade --reuse-values carries the stored values
  forward and does NOT merge in defaults for keys the chart has grown since.
  sslRootCert arrived in 0.8.0, so an install predating it upgraded with
  --reuse-values had no such map and this dereference failed the render with
  "nil pointer evaluating interface {}.existingSecret" — an upgrade path
  broken for exactly the operators who had been running longest.
*/ -}}
{{- $ca := .Values.database.postgres.sslRootCert | default dict }}
{{- if $ca.existingSecret }}
- name: POSTGRES_SSLROOTCERT
  value: /etc/dencer/postgres-ca/{{ $ca.key | default "ca.crt" }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
Whether the plan store is a file this pod has to mount.

True only for SQLite. Postgres is reached over the network, so the data
volume, the claim behind it, the co-scheduling affinity and the Recreate
strategy all stop being necessary — those exist to keep two writers off one
file, and there is no file. Mounting a claim anyway would quietly reimpose the
single-node constraint that choosing Postgres was meant to lift.
*/}}
{{- define "k8s-dencer.mountsDataVolume" -}}
{{- if eq .Values.database.type "sqlite" }}true{{ end -}}
{{- end -}}

{{/*
Whether a CA bundle is mounted for verifying the Postgres server certificate.

Without one, sslMode=require accepts any certificate the server presents: the
channel is encrypted but the peer is not authenticated, so an attacker on the
network path can impersonate the database and collect the credentials, the
audit trail and the run queue the executor drains from.
*/}}
{{- define "k8s-dencer.mountsPostgresCA" -}}
{{- $ca := .Values.database.postgres.sslRootCert | default dict -}}
{{- if and (eq .Values.database.type "postgres") $ca.existingSecret }}true{{ end -}}
{{- end -}}

{{- define "k8s-dencer.postgresCAVolume" -}}
{{- if include "k8s-dencer.mountsPostgresCA" . }}
- name: postgres-ca
  secret:
    secretName: {{ (.Values.database.postgres.sslRootCert | default dict).existingSecret }}
{{- end }}
{{- end -}}

{{- define "k8s-dencer.postgresCAMount" -}}
{{- if include "k8s-dencer.mountsPostgresCA" . }}
- name: postgres-ca
  mountPath: /etc/dencer/postgres-ca
  readOnly: true
{{- end }}
{{- end -}}
