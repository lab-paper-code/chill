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

{{- define "chill.operatorName" -}}
{{- printf "%s-operator" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.operatorSelectorLabels" -}}
app.kubernetes.io/name: {{ include "chill.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: operator
{{- end -}}

{{- define "chill.operatorServiceAccountName" -}}
{{- if .Values.operator.serviceAccount.create -}}
{{- default (include "chill.operatorName" .) .Values.operator.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.operator.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "chill.operatorImage" -}}
{{- printf "%s:%s" .Values.operator.image.repository (include "chill.operatorImageTag" .) -}}
{{- end -}}

{{- define "chill.operatorImageTag" -}}
{{- default .Chart.AppVersion .Values.operator.image.tag -}}
{{- end -}}

{{- define "chill.profilerName" -}}
{{- printf "%s-profiler" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.profilerConfigName" -}}
{{- printf "%s-profiler-config" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.profilerFactSourceName" -}}
{{- printf "%s-profiler-facts" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.profilerServiceAccountName" -}}
{{- if .Values.profiler.serviceAccount.create -}}
{{- default (include "chill.profilerName" .) .Values.profiler.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.profiler.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "chill.profilerWorkloadObserverServiceAccountName" -}}
{{- if .Values.profiler.workloadObserver.serviceAccount.create -}}
{{- default (printf "%s-workload-observer" (include "chill.profilerName" .)) .Values.profiler.workloadObserver.serviceAccount.name -}}
{{- else -}}
{{- .Values.profiler.workloadObserver.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "chill.profilerWorkloadObserverKubernetesAPIServer" -}}
{{- if .Values.profiler.workloadObserver.kubernetesClient.apiServer -}}
{{- .Values.profiler.workloadObserver.kubernetesClient.apiServer -}}
{{- else if .Values.profiler.workloadObserver.kubernetesClient.discoverFromDefaultEndpoint -}}
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

{{- define "chill.profilerImage" -}}
{{- printf "%s:%s" .Values.profiler.image.repository (include "chill.profilerImageTag" .) -}}
{{- end -}}

{{- define "chill.profilerImageTag" -}}
{{- default .Chart.AppVersion .Values.profiler.image.tag -}}
{{- end -}}

{{- define "chill.nodeActuatorName" -}}
{{- printf "%s-node-actuator" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.nodeActuatorConfigName" -}}
{{- printf "%s-node-actuator-config" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.nodeActuatorServiceAccountName" -}}
{{- if .Values.nodeActuator.serviceAccount.create -}}
{{- default (include "chill.nodeActuatorName" .) .Values.nodeActuator.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.nodeActuator.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "chill.nodeActuatorImageTag" -}}
{{- default .Chart.AppVersion .Values.nodeActuator.image.tag -}}
{{- end -}}

{{- define "chill.nodeActuatorImage" -}}
{{- printf "%s:%s" .Values.nodeActuator.image.repository (include "chill.nodeActuatorImageTag" .) -}}
{{- end -}}

{{- define "chill.nodeActuatorKubernetesAPIServer" -}}
{{- if .Values.nodeActuator.kubernetesClient.apiServer -}}
{{- .Values.nodeActuator.kubernetesClient.apiServer -}}
{{- else if .Values.nodeActuator.kubernetesClient.discoverFromDefaultEndpoint -}}
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

{{- define "chill.consoleName" -}}
{{- printf "%s-console" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.consoleServiceAccountName" -}}
{{- if .Values.console.serviceAccount.create -}}
{{- default (include "chill.consoleName" .) .Values.console.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.console.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "chill.consoleImage" -}}
{{- printf "%s:%s" .Values.console.image.repository (include "chill.consoleImageTag" .) -}}
{{- end -}}

{{- define "chill.consoleImageTag" -}}
{{- default .Chart.AppVersion .Values.console.image.tag -}}
{{- end -}}

{{- define "chill.servingEntrypointName" -}}
{{- printf "%s-serving-entrypoint" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.servingEntrypointImage" -}}
{{- printf "%s:%s" .Values.servingEntrypoint.image.repository (include "chill.servingEntrypointImageTag" .) -}}
{{- end -}}

{{- define "chill.servingEntrypointImageTag" -}}
{{- default .Chart.AppVersion .Values.servingEntrypoint.image.tag -}}
{{- end -}}

{{- define "chill.uninstallCleanupName" -}}
{{- printf "%s-uninstall-cleanup" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.uninstallCleanupServiceAccountName" -}}
{{- if .Values.uninstallCleanup.serviceAccount.create -}}
{{- default (include "chill.uninstallCleanupName" .) .Values.uninstallCleanup.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.uninstallCleanup.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "chill.uninstallCleanupImage" -}}
{{- printf "%s:%s" .Values.uninstallCleanup.image.repository .Values.uninstallCleanup.image.tag -}}
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
