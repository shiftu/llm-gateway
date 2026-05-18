{{/*
Expand the name of the chart.
*/}}
{{- define "llm-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "llm-gateway.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "llm-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "llm-gateway.labels" -}}
helm.sh/chart: {{ include "llm-gateway.chart" . }}
{{ include "llm-gateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "llm-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "llm-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Return the image tag to use (defaults to Chart.AppVersion).
*/}}
{{- define "llm-gateway.imageTag" -}}
{{- .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Return the name of the auth Secret.
When auth.existingSecret is set, use that; otherwise use the chart-managed secret.
*/}}
{{- define "llm-gateway.authSecretName" -}}
{{- if .Values.auth.existingSecret }}
{{- .Values.auth.existingSecret }}
{{- else }}
{{- printf "%s-auth" (include "llm-gateway.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Return the key within the auth Secret.
*/}}
{{- define "llm-gateway.authSecretKey" -}}
{{- .Values.auth.existingSecretKey | default "LLM_GATEWAY_TOKEN" }}
{{- end }}

{{/*
Return the name of the masterKey Secret.
When masterKey.existingSecret is set, use that; otherwise use the chart-managed secret.
*/}}
{{- define "llm-gateway.masterKeySecretName" -}}
{{- if .Values.masterKey.existingSecret }}
{{- .Values.masterKey.existingSecret }}
{{- else }}
{{- printf "%s-master-key" (include "llm-gateway.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Return the key within the masterKey Secret.
*/}}
{{- define "llm-gateway.masterKeySecretKey" -}}
{{- .Values.masterKey.existingSecretKey | default "LLM_GATEWAY_MASTER_KEY" }}
{{- end }}

{{/*
Return the PVC name for persistent data.
*/}}
{{- define "llm-gateway.pvcName" -}}
{{- printf "%s-data" .Release.Name }}
{{- end }}
