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

{{- define "chill.profilerTransitionObserverServiceAccountName" -}}
{{- if .Values.profiler.transitionObserver.serviceAccount.create -}}
{{- $defaultName := printf "%s-transition-observer" (include "chill.profilerName" .) -}}
{{- default $defaultName .Values.profiler.transitionObserver.serviceAccount.name -}}
{{- else -}}
{{- .Values.profiler.transitionObserver.serviceAccount.name -}}
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

{{- define "chill.nodeAgentName" -}}
{{- printf "%s-node-agent" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.nodeAgentActuationName" -}}
{{- printf "%s-node-agent-actuation" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.nodeAgentConfigName" -}}
{{- printf "%s-node-agent-config" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.nodeAgentSignatureCatalogName" -}}
{{- printf "%s-node-agent-signatures" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.nodeAgentSignatureKey" -}}
signatures.yaml
{{- end -}}

{{- define "chill.nodeAgentImageTag" -}}
{{- default .Chart.AppVersion .Values.nodeAgent.image.tag -}}
{{- end -}}

{{- define "chill.nodeAgentImage" -}}
{{- printf "%s:%s" .Values.nodeAgent.image.repository (include "chill.nodeAgentImageTag" .) -}}
{{- end -}}

{{- define "chill.nodeAgentKubernetesAPIServer" -}}
{{- if .Values.nodeAgent.kubernetesClient.apiServer -}}
{{- .Values.nodeAgent.kubernetesClient.apiServer -}}
{{- else if .Values.nodeAgent.kubernetesClient.discoverFromDefaultEndpoint -}}
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

{{- define "chill.dashboardName" -}}
{{- printf "%s-dashboard" (include "chill.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "chill.dashboardImage" -}}
{{- printf "%s:%s" .Values.dashboard.image.repository (default .Chart.AppVersion .Values.dashboard.image.tag) -}}
{{- end -}}

{{- define "chill.dashboardBaseURL" -}}
{{- if .Values.dashboard.baseURL -}}
{{- .Values.dashboard.baseURL -}}
{{- else if .Values.servingEntrypoint.enabled -}}
{{- printf "https://%s.%s.svc.cluster.local:%v" (include "chill.servingEntrypointName" .) .Release.Namespace .Values.servingEntrypoint.port -}}
{{- end -}}
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

{{- define "chill.discoveryCatalogName" -}}
{{- default (printf "%s-device-catalog" (include "chill.fullname" .)) .Values.discovery.catalog.name -}}
{{- end -}}

{{- define "chill.discoveryCatalogKey" -}}
{{- default "catalog.yaml" .Values.discovery.catalog.key -}}
{{- end -}}

{{- define "chill.discoveryNodeLabelSelector" -}}
{{- default .Values.nodeSelection.labelSelector .Values.discovery.nodeLabelSelector -}}
{{- end -}}

{{- define "chill.systemName" -}}
{{- default .Release.Name .Values.system.name -}}
{{- end -}}

