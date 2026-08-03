{{/*
Expand the name of the chart.
*/}}
{{- define "shadow-diff.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "shadow-diff.fullname" -}}
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

{{- define "shadow-diff.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "shadow-diff.labels" -}}
helm.sh/chart: {{ include "shadow-diff.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: shadow-diff
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{- define "shadow-diff.selectorLabels" -}}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "shadow-diff.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "shadow-diff.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* Image helpers: registry/repo:tag */}}
{{- define "shadow-diff.image" -}}
{{- $root := index . 0 -}}
{{- $repo := index . 1 -}}
{{- $tag := index . 2 -}}
{{- $registry := $root.Values.global.imageRegistry -}}
{{- $resolvedTag := $tag | default $root.Values.global.imageTag | default "latest" -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repo $resolvedTag -}}
{{- else -}}
{{- printf "%s:%s" $repo $resolvedTag -}}
{{- end -}}
{{- end }}

{{- define "shadow-diff.monarchImage" -}}
{{- include "shadow-diff.image" (list . .Values.monarch.image.repository .Values.monarch.image.tag) }}
{{- end }}

{{- define "shadow-diff.tuskImage" -}}
{{- include "shadow-diff.image" (list . .Values.tusk.image.repository .Values.tusk.image.tag) }}
{{- end }}

{{- define "shadow-diff.theSystemImage" -}}
{{- include "shadow-diff.image" (list . .Values.theSystem.image.repository .Values.theSystem.image.tag) }}
{{- end }}

{{- define "shadow-diff.helperImage" -}}
{{- $root := index . 0 -}}
{{- $name := index . 1 -}}
{{- $override := index . 2 -}}
{{- if $override -}}
{{- $override -}}
{{- else -}}
{{- include "shadow-diff.image" (list $root $name "") -}}
{{- end -}}
{{- end }}

{{- define "shadow-diff.pullPolicy" -}}
{{- $root := index . 0 -}}
{{- $override := index . 1 -}}
{{- $override | default $root.Values.global.imagePullPolicy | default "IfNotPresent" -}}
{{- end }}

{{- define "shadow-diff.s3SecretName" -}}
{{- if .Values.aws.createSecret -}}
{{- printf "%s-s3" (include "shadow-diff.fullname" .) -}}
{{- else -}}
{{- .Values.aws.existingSecret -}}
{{- end -}}
{{- end }}

{{- define "shadow-diff.postgresSecretName" -}}
{{- if .Values.postgres.createSecret -}}
{{- printf "%s-postgres" (include "shadow-diff.fullname" .) -}}
{{- else -}}
{{- .Values.postgres.existingSecret -}}
{{- end -}}
{{- end }}

{{- define "shadow-diff.beruDbSecret" -}}
{{- if .Values.monarch.beruDbSecret -}}
{{- .Values.monarch.beruDbSecret -}}
{{- else -}}
{{- printf "%s/%s" .Release.Namespace (include "shadow-diff.postgresSecretName" .) -}}
{{- end -}}
{{- end }}

{{- define "shadow-diff.monarchName" -}}
{{- printf "%s-monarch" (include "shadow-diff.fullname" .) -}}
{{- end }}

{{- define "shadow-diff.monarchStatusService" -}}
{{- printf "%s-monarch-status-grpc" (include "shadow-diff.fullname" .) -}}
{{- end }}

{{- define "shadow-diff.tuskName" -}}
{{- printf "%s-tusk" (include "shadow-diff.fullname" .) -}}
{{- end }}

{{- define "shadow-diff.theSystemName" -}}
{{- printf "%s-the-system" (include "shadow-diff.fullname" .) -}}
{{- end }}

{{- define "shadow-diff.monarchGrpcAddr" -}}
{{- if .Values.tusk.monarchGrpcAddr -}}
{{- .Values.tusk.monarchGrpcAddr -}}
{{- else -}}
{{- printf "%s.%s.svc.cluster.local:9090" (include "shadow-diff.monarchStatusService" .) .Release.Namespace -}}
{{- end -}}
{{- end }}
