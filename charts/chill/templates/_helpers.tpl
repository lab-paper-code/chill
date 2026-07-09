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

{{- define "chill.nodeDiscoveryConfigName" -}}
{{- printf "%s-node-discovery-config" (include "chill.fullname" .) -}}
{{- end -}}

{{- define "chill.systemName" -}}
{{- default .Release.Name .Values.system.name -}}
{{- end -}}

{{- define "chill.nodeDiscoverySignatureKey" -}}
signatures.yaml
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

{{- define "chill.nodeDiscoveryKubernetesAPIServer" -}}
{{- if .Values.nodeDiscovery.kubernetesClient.apiServer -}}
{{- .Values.nodeDiscovery.kubernetesClient.apiServer -}}
{{- else if .Values.nodeDiscovery.kubernetesClient.discoverFromDefaultEndpoint -}}
{{- $endpoint := lookup "v1" "Endpoints" "default" "kubernetes" -}}
{{- if and $endpoint $endpoint.subsets -}}
{{- $subset := index $endpoint.subsets 0 -}}
{{- if and $subset.addresses $subset.ports -}}
{{- $address := index $subset.addresses 0 -}}
{{- $port := index $subset.ports 0 -}}
{{- if and $address.ip $port.port -}}
{{- printf "https://%s:%v" $address.ip $port.port -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
