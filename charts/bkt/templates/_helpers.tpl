{{/*
Expand the name of the chart.
*/}}
{{- define "bkt.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bkt.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "bkt.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "bkt.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "bkt.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bkt.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Image tag — use per-component override if set, else global.imageTag
*/}}
{{- define "bkt.backendTag" -}}
{{- .Values.backend.image.tag | default .Values.global.imageTag }}
{{- end }}

{{/*
Image pull policy — per-component or global
*/}}
{{- define "bkt.backendPullPolicy" -}}
{{- .Values.backend.image.pullPolicy | default .Values.global.imagePullPolicy }}
{{- end }}

{{/*
Database host — bitnami subchart or external
*/}}
{{- define "bkt.databaseHost" -}}
{{- if .Values.postgresql.enabled }}
{{- printf "%s-postgresql" .Release.Name }}
{{- else }}
{{- .Values.externalDatabase.host }}
{{- end }}
{{- end }}

{{- define "bkt.databasePort" -}}
{{- if .Values.postgresql.enabled }}5432{{- else }}{{- .Values.externalDatabase.port }}{{- end }}
{{- end }}

{{- define "bkt.databaseName" -}}
{{- if .Values.postgresql.enabled }}{{- .Values.postgresql.auth.database }}{{- else }}{{- .Values.externalDatabase.database }}{{- end }}
{{- end }}

{{- define "bkt.databaseUser" -}}
{{- if .Values.postgresql.enabled }}{{- .Values.postgresql.auth.username }}{{- else }}{{- .Values.externalDatabase.username }}{{- end }}
{{- end }}

{{/*
backend.env.<KEY> as a string, falling back to a default only when the key is
unset or "". Unlike `default`, a deliberate falsy value (false, 0) is kept, so
`--set backend.env.HSTS_MAX_AGE=0` really disables HSTS. YAML numbers arrive
as float64 and are rendered as integers.
Usage: {{ include "bkt.env" (list . "HSTS_MAX_AGE" "31536000") }}
*/}}
{{- define "bkt.env" -}}
{{- $root := index . 0 }}
{{- $v := index $root.Values.backend.env (index . 1) }}
{{- if or (kindIs "invalid" $v) (and (kindIs "string" $v) (eq $v "")) }}
{{- index . 2 }}
{{- else if kindIs "float64" $v }}
{{- $v | int64 | toString }}
{{- else }}
{{- $v | toString }}
{{- end }}
{{- end }}

{{/*
Trusted proxies for X-Forwarded-For. An explicit value always wins; with an
ingress and no value, trust the private/CGNAT ranges ingress controllers run in
so per-IP rate limiting sees real clients (see values.yaml for the trade-off).
*/}}
{{- define "bkt.trustedProxies" -}}
{{- if .Values.backend.env.TRUSTED_PROXIES }}
{{- .Values.backend.env.TRUSTED_PROXIES }}
{{- else if .Values.ingress.enabled }}
{{- "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,fc00::/7" }}
{{- end }}
{{- end }}
