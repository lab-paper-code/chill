{{- define "chill.validateValues" -}}
{{- $nodeDiscoveryPlaceholderRepository := "chill/node-discovery" -}}
{{- $nodeDiscoveryPlaceholderTag := "latest" -}}
{{- $nodeDiscoveryImageTag := include "chill.nodeDiscoveryImageTag" . -}}
{{- $operatorImageTag := include "chill.operatorImageTag" . -}}
{{- $profilerImageTag := include "chill.profilerImageTag" . -}}
{{- $consoleImageTag := include "chill.consoleImageTag" . -}}
{{- if empty (include "chill.systemName" .) -}}
{{- fail "system.name or release name must not be empty" -}}
{{- end -}}
{{- if and .Values.evidenceStore.enabled (empty .Values.evidenceStore.configRef.name) -}}
{{- fail "evidenceStore.configRef.name must not be empty when evidenceStore.enabled=true" -}}
{{- end -}}
{{- if .Values.console.enabled -}}
{{- if lt (int .Values.console.replicaCount) 1 -}}
{{- fail "console.replicaCount must be positive when console.enabled=true" -}}
{{- end -}}
{{- if empty .Values.console.image.repository -}}
{{- fail "console.image.repository must not be empty when console.enabled=true" -}}
{{- end -}}
{{- if empty $consoleImageTag -}}
{{- fail "console.image.tag or Chart.appVersion must not be empty when console.enabled=true" -}}
{{- end -}}
{{- if or (lt (int .Values.console.port) 1) (gt (int .Values.console.port) 65535) -}}
{{- fail "console.port must be between 1 and 65535 when console.enabled=true" -}}
{{- end -}}
{{- if and (not .Values.console.serviceAccount.create) (empty .Values.console.serviceAccount.name) -}}
{{- fail "console.serviceAccount.name must be set when console.serviceAccount.create=false" -}}
{{- end -}}
{{- end -}}
{{- if empty .Values.operator.image.repository -}}
{{- fail "operator.image.repository must not be empty" -}}
{{- end -}}
{{- if empty $operatorImageTag -}}
{{- fail "operator.image.tag or Chart.appVersion must not be empty" -}}
{{- end -}}
{{- if empty .Values.operator.image.pullPolicy -}}
{{- fail "operator.image.pullPolicy must not be empty" -}}
{{- end -}}
{{- if empty .Values.operator.nodeDiscoveryReconcileInterval -}}
{{- fail "operator.nodeDiscoveryReconcileInterval must not be empty" -}}
{{- end -}}
{{- if and .Values.operator.leaderElection (empty .Values.operator.leaderElectionID) -}}
{{- fail "operator.leaderElectionID must not be empty when operator.leaderElection=true" -}}
{{- end -}}
{{- if and (not .Values.operator.serviceAccount.create) (empty .Values.operator.serviceAccount.name) -}}
{{- fail "operator.serviceAccount.name must be set when operator.serviceAccount.create=false" -}}
{{- end -}}
{{- if .Values.profiler.enabled -}}
{{- if lt (int .Values.profiler.replicaCount) 1 -}}
{{- fail "profiler.replicaCount must be positive when profiler.enabled=true" -}}
{{- end -}}
{{- if empty .Values.profiler.configMapKey -}}
{{- fail "profiler.configMapKey must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if empty .Values.profiler.factSource.executionFactsKey -}}
{{- fail "profiler.factSource.executionFactsKey must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if empty .Values.profiler.factSource.metricSourcesKey -}}
{{- fail "profiler.factSource.metricSourcesKey must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if empty .Values.profiler.factSource.workloadObservationKey -}}
{{- fail "profiler.factSource.workloadObservationKey must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if or (eq .Values.profiler.factSource.executionFactsKey .Values.profiler.factSource.metricSourcesKey) (eq .Values.profiler.factSource.executionFactsKey .Values.profiler.factSource.workloadObservationKey) (eq .Values.profiler.factSource.metricSourcesKey .Values.profiler.factSource.workloadObservationKey) -}}
{{- fail "profiler.factSource keys must be different" -}}
{{- end -}}
{{- if not (kindIs "slice" .Values.profiler.factSource.executionDeclarations) -}}
{{- fail "profiler.factSource.executionDeclarations must be a list" -}}
{{- end -}}
{{- if and .Values.profiler.factSource.executionDeclarations (empty .Values.profiler.factSource.metricObserverImage) -}}
{{- fail "profiler.factSource.metricObserverImage is required when execution declarations are configured" -}}
{{- end -}}
{{- if and .Values.profiler.factSource.metricObserverImage (not (regexMatch "^[^@[:space:]]+@sha256:[0-9a-f]{64}$" .Values.profiler.factSource.metricObserverImage)) -}}
{{- fail "profiler.factSource.metricObserverImage must be digest-pinned" -}}
{{- end -}}
{{- if and .Values.profiler.factSource.executionDeclarations (empty .Values.profiler.factSource.workloadObserverImage) -}}
{{- fail "profiler.factSource.workloadObserverImage is required when execution declarations are configured" -}}
{{- end -}}
{{- if and .Values.profiler.factSource.workloadObserverImage (not (regexMatch "^[^@[:space:]]+@sha256:[0-9a-f]{64}$" .Values.profiler.factSource.workloadObserverImage)) -}}
{{- fail "profiler.factSource.workloadObserverImage must be digest-pinned" -}}
{{- end -}}
{{- if not (kindIs "slice" .Values.profiler.factSource.allowedBackgroundSelectors) -}}
{{- fail "profiler.factSource.allowedBackgroundSelectors must be a list" -}}
{{- end -}}
{{- if not (kindIs "slice" .Values.profiler.factSource.metricSources) -}}
{{- fail "profiler.factSource.metricSources must be a list" -}}
{{- end -}}
{{- if empty .Values.profiler.image.repository -}}
{{- fail "profiler.image.repository must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if empty $profilerImageTag -}}
{{- fail "profiler.image.tag or Chart.appVersion must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if empty .Values.profiler.image.pullPolicy -}}
{{- fail "profiler.image.pullPolicy must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if empty .Values.profiler.metricsBindAddress -}}
{{- fail "profiler.metricsBindAddress must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if or (lt (int .Values.profiler.healthProbePort) 1) (gt (int .Values.profiler.healthProbePort) 65535) -}}
{{- fail "profiler.healthProbePort must be between 1 and 65535 when profiler.enabled=true" -}}
{{- end -}}
{{- if and .Values.profiler.leaderElection (empty .Values.profiler.leaderElectionID) -}}
{{- fail "profiler.leaderElectionID must not be empty when profiler.leaderElection=true" -}}
{{- end -}}
{{- if and (not .Values.profiler.serviceAccount.create) (empty .Values.profiler.serviceAccount.name) -}}
{{- fail "profiler.serviceAccount.name must be set when profiler.serviceAccount.create=false" -}}
{{- end -}}
{{- end -}}
{{- if .Values.uninstallCleanup.enabled -}}
{{- if empty .Values.uninstallCleanup.image.repository -}}
{{- fail "uninstallCleanup.image.repository must not be empty when uninstallCleanup.enabled=true" -}}
{{- end -}}
{{- if empty .Values.uninstallCleanup.image.tag -}}
{{- fail "uninstallCleanup.image.tag must not be empty when uninstallCleanup.enabled=true" -}}
{{- end -}}
{{- if empty .Values.uninstallCleanup.image.pullPolicy -}}
{{- fail "uninstallCleanup.image.pullPolicy must not be empty when uninstallCleanup.enabled=true" -}}
{{- end -}}
{{- if empty .Values.uninstallCleanup.timeout -}}
{{- fail "uninstallCleanup.timeout must not be empty when uninstallCleanup.enabled=true" -}}
{{- end -}}
{{- if and (not .Values.uninstallCleanup.serviceAccount.create) (empty .Values.uninstallCleanup.serviceAccount.name) -}}
{{- fail "uninstallCleanup.serviceAccount.name must be set when uninstallCleanup.serviceAccount.create=false" -}}
{{- end -}}
{{- end -}}
{{- if and .Values.discovery.enabled .Values.discovery.requireCatalogMatch (not .Values.discovery.catalog.enabled) (empty .Values.discovery.catalog.name) -}}
{{- fail "discovery.catalog.name must be set when discovery.requireCatalogMatch=true and discovery.catalog.enabled=false" -}}
{{- end -}}
{{- if and .Values.discovery.enabled .Values.discovery.requireCatalogMatch .Values.discovery.catalog.enabled (empty .Values.discovery.catalog.classes) -}}
{{- fail "discovery.catalog.classes must contain at least one class when discovery.requireCatalogMatch=true" -}}
{{- end -}}
{{- if and .Values.discovery.enabled (not .Values.discovery.requireCatalogMatch) (empty .Values.discovery.fallbackPowerModes) -}}
{{- fail "discovery.fallbackPowerModes must contain at least one mode when discovery.requireCatalogMatch=false" -}}
{{- end -}}
{{- if and .Values.discovery.enabled .Values.discovery.fallbackPowerModes -}}
{{- range $index, $mode := .Values.discovery.fallbackPowerModes -}}
{{- if not (kindIs "map" $mode) -}}
{{- fail (printf "discovery.fallbackPowerModes[%d].name must be set" $index) -}}
{{- end -}}
{{- if empty $mode.name -}}
{{- fail (printf "discovery.fallbackPowerModes[%d].name must be set" $index) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if .Values.nodeDiscovery.enabled -}}
{{- if empty .Values.nodeDiscovery.configMapKey -}}
{{- fail "nodeDiscovery.configMapKey must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.image.repository -}}
{{- fail "nodeDiscovery.image.repository must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty $nodeDiscoveryImageTag -}}
{{- fail "nodeDiscovery.image.tag or Chart.appVersion must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.image.pullPolicy -}}
{{- fail "nodeDiscovery.image.pullPolicy must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.updateStrategy.type -}}
{{- fail "nodeDiscovery.updateStrategy.type must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if and .Values.nodeDiscovery.excludeNodeNames .Values.nodeDiscovery.affinity.nodeAffinity -}}
{{- fail "nodeDiscovery.excludeNodeNames cannot be used together with nodeDiscovery.affinity.nodeAffinity" -}}
{{- end -}}
{{- if and .Values.nodeDiscovery.kubernetesClient (empty .Values.nodeDiscovery.kubernetesClient.tokenFile) -}}
{{- fail "nodeDiscovery.kubernetesClient.tokenFile must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if and .Values.nodeDiscovery.kubernetesClient (empty .Values.nodeDiscovery.kubernetesClient.caFile) -}}
{{- fail "nodeDiscovery.kubernetesClient.caFile must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if and (eq .Values.nodeDiscovery.image.repository $nodeDiscoveryPlaceholderRepository) (eq $nodeDiscoveryImageTag $nodeDiscoveryPlaceholderTag) (ne .Values.nodeDiscovery.image.pullPolicy "Never") -}}
{{- fail "nodeDiscovery image chill/node-discovery:latest is a local development placeholder, not a published runtime image; set nodeDiscovery.image.repository/tag to a published image or use nodeDiscovery.image.pullPolicy=Never with a node-local image" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.sources -}}
{{- fail "nodeDiscovery.sources must contain at least one source when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.signatures -}}
{{- fail "nodeDiscovery.signatures must contain at least one signature when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- end -}}
{{- end -}}
