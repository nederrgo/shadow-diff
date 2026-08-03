{{- define "shadow-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "shadow-agent.fullname" -}}
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

{{- define "shadow-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "shadow-agent.labels" -}}
helm.sh/chart: {{ include "shadow-agent.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: shadow-diff
app.kubernetes.io/name: kaisel
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{- define "shadow-agent.selectorLabels" -}}
app: kaisel
app.kubernetes.io/name: kaisel
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "shadow-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "shadow-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "shadow-agent.image" -}}
{{- $registry := .Values.global.imageRegistry -}}
{{- $repo := .Values.kaisel.image.repository -}}
{{- $tag := .Values.kaisel.image.tag | default .Values.global.imageTag | default "latest" -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repo $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end }}

{{- define "shadow-agent.pullPolicy" -}}
{{- .Values.kaisel.image.pullPolicy | default .Values.global.imagePullPolicy | default "IfNotPresent" -}}
{{- end }}
