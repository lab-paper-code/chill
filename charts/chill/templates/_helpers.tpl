{{- define "chill.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "chill.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "chill.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "chill.selectorLabels" -}}
app.kubernetes.io/name: {{ include "chill.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "chill.controllerServiceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (printf "%s-controller-manager" (include "chill.fullname" .)) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "chill.serviceAccountName" -}}
{{- include "chill.controllerServiceAccountName" . -}}
{{- end -}}

{{- define "chill.nodeDiscoveryServiceAccountName" -}}
{{- if .Values.nodeDiscovery.serviceAccount.create -}}
{{- default (printf "%s-node-discovery" (include "chill.fullname" .)) .Values.nodeDiscovery.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.nodeDiscovery.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "chill.discoveryCatalogName" -}}
{{- default (printf "%s-device-catalog" (include "chill.fullname" .)) .Values.discovery.catalog.name -}}
{{- end -}}

{{- define "chill.discoveryCatalogKey" -}}
{{- default "catalog.yaml" .Values.discovery.catalog.key -}}
{{- end -}}

{{- define "chill.discoveryNodeLabelSelector" -}}
{{- default .Values.nodeSelection.labelSelector .Values.discovery.nodeLabelSelector -}}
{{- end -}}

{{- define "chill.nodeDiscoverySignatureCatalogName" -}}
{{- printf "%s-node-discovery-signatures" (include "chill.fullname" .) -}}
{{- end -}}

{{- define "chill.systemStatusName" -}}
{{- default .Release.Name .Values.systemStatus.name -}}
{{- end -}}

{{- define "chill.nodeDiscoverySignatureKey" -}}
signatures.yaml
{{- end -}}

{{- define "chill.controllerImage" -}}
{{- printf "%s:%s" .Values.controller.image.repository (include "chill.controllerImageTag" .) -}}
{{- end -}}

{{- define "chill.controllerImageTag" -}}
{{- default .Chart.AppVersion .Values.controller.image.tag -}}
{{- end -}}

{{- define "chill.nodeDiscoveryImage" -}}
{{- printf "%s:%s" .Values.nodeDiscovery.image.repository (include "chill.nodeDiscoveryImageTag" .) -}}
{{- end -}}

{{- define "chill.nodeDiscoveryImageTag" -}}
{{- default .Chart.AppVersion .Values.nodeDiscovery.image.tag -}}
{{- end -}}

{{- define "chill.nodeDiscoveryImagePullPolicy" -}}
{{- .Values.nodeDiscovery.image.pullPolicy -}}
{{- end -}}
