{{- define "chill.validateValues" -}}
{{- $nodeAgentPlaceholderRepository := "chill/node-agent" -}}
{{- $nodeAgentPlaceholderTag := "latest" -}}
{{- $nodeAgentImageTag := include "chill.nodeAgentImageTag" . -}}
{{- $operatorImageTag := include "chill.operatorImageTag" . -}}
{{- $profilerImageTag := include "chill.profilerImageTag" . -}}
{{- $consoleImageTag := include "chill.consoleImageTag" . -}}
{{- $servingEntrypointImageTag := include "chill.servingEntrypointImageTag" . -}}
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
{{- if .Values.servingEntrypoint.enabled -}}
{{- if empty .Values.servingEntrypoint.image.repository -}}
{{- fail "servingEntrypoint.image.repository must not be empty when servingEntrypoint.enabled=true" -}}
{{- end -}}
{{- if empty $servingEntrypointImageTag -}}
{{- fail "servingEntrypoint.image.tag or Chart.appVersion must not be empty when servingEntrypoint.enabled=true" -}}
{{- end -}}
{{- if or (lt (int .Values.servingEntrypoint.port) 1) (gt (int .Values.servingEntrypoint.port) 65535) -}}
{{- fail "servingEntrypoint.port must be between 1 and 65535 when servingEntrypoint.enabled=true" -}}
{{- end -}}
{{- if empty .Values.servingEntrypoint.tls.secretName -}}
{{- fail "servingEntrypoint.tls.secretName must name an existing TLS Secret when servingEntrypoint.enabled=true" -}}
{{- end -}}
{{- if or (empty .Values.evidenceStore.connection.bucket) (empty .Values.evidenceStore.connection.region) -}}
{{- fail "evidenceStore.connection.bucket and region must match the evidence store when servingEntrypoint.enabled=true" -}}
{{- end -}}
{{- if empty .Values.servingEntrypoint.observationStorage.size -}}
{{- fail "servingEntrypoint.observationStorage.size must not be empty when servingEntrypoint.enabled=true" -}}
{{- end -}}
{{- end -}}
{{- if .Values.dashboard.enabled -}}
{{- if or (lt (int .Values.dashboard.replicaCount) 1) (empty .Values.dashboard.image.repository) -}}
{{- fail "dashboard requires a positive replicaCount and image.repository" -}}
{{- end -}}
{{- if or (lt (int .Values.dashboard.port) 1) (gt (int .Values.dashboard.port) 65535) -}}
{{- fail "dashboard.port must be between 1 and 65535" -}}
{{- end -}}
{{- if not (regexMatch "^https://[^/?#@]+/?$" (include "chill.dashboardBaseURL" .)) -}}
{{- fail "dashboard requires a fixed HTTPS baseURL or an enabled servingEntrypoint" -}}
{{- end -}}
{{- if and .Values.dashboard.caSecretName (empty .Values.dashboard.caSecretKey) -}}
{{- fail "dashboard.caSecretKey is required with caSecretName" -}}
{{- end -}}
{{- if .Values.dashboard.ingress.enabled -}}
{{- if or (empty .Values.dashboard.ingress.host) (empty .Values.dashboard.ingress.tlsSecretName) -}}
{{- fail "dashboard ingress requires host and tlsSecretName; bearer traffic must use HTTPS" -}}
{{- end -}}
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
{{- if empty .Values.operator.nodeAgentReconcileInterval -}}
{{- fail "operator.nodeAgentReconcileInterval must not be empty" -}}
{{- end -}}
{{- if and .Values.operator.leaderElection (empty .Values.operator.leaderElectionID) -}}
{{- fail "operator.leaderElectionID must not be empty when operator.leaderElection=true" -}}
{{- end -}}
{{- if and (not .Values.operator.serviceAccount.create) (empty .Values.operator.serviceAccount.name) -}}
{{- fail "operator.serviceAccount.name must be set when operator.serviceAccount.create=false" -}}
{{- end -}}
{{- if and .Values.nodeAgent.actuation.enabled (not .Values.nodeAgent.enabled) -}}
{{- fail "nodeAgent.actuation.enabled=true requires nodeAgent.enabled=true" -}}
{{- end -}}
{{- if and .Values.nodeAgent.enabled .Values.nodeAgent.actuation.enabled -}}
{{- $actuation := .Values.nodeAgent.actuation -}}
{{- if empty $actuation.metricsBindAddress -}}
{{- fail "nodeAgent.actuation.metricsBindAddress must not be empty when nodeAgent.actuation.enabled=true" -}}
{{- end -}}
{{- if or (lt (int $actuation.healthProbePort) 1) (gt (int $actuation.healthProbePort) 65535) -}}
{{- fail "nodeAgent.actuation.healthProbePort must be between 1 and 65535 when nodeAgent.actuation.enabled=true" -}}
{{- end -}}
{{- if empty $actuation.updateStrategy.type -}}
{{- fail "nodeAgent.actuation.updateStrategy.type must not be empty when nodeAgent.actuation.enabled=true" -}}
{{- end -}}
{{- if empty .Values.powerActuation.policies -}}
{{- fail "powerActuation.policies must name at least one DeviceClass when nodeAgent.actuation.enabled=true" -}}
{{- end -}}
{{- if empty $actuation.jetsonNVPModel.command -}}
{{- fail "nodeAgent.actuation.jetsonNVPModel.command must not be empty when nodeAgent.actuation.enabled=true" -}}
{{- end -}}
{{- if empty $actuation.jetsonNVPModel.commandTimeout -}}
{{- fail "nodeAgent.actuation.jetsonNVPModel.commandTimeout must not be empty when nodeAgent.actuation.enabled=true" -}}
{{- end -}}
{{- if empty $actuation.jetsonNVPModel.hostPaths -}}
{{- fail "nodeAgent.actuation.jetsonNVPModel.hostPaths must not be empty when nodeAgent.actuation.enabled=true" -}}
{{- end -}}
{{- if empty $actuation.nvidiaLockedGPUClock.command -}}
{{- fail "nodeAgent.actuation.nvidiaLockedGPUClock.command must not be empty" -}}
{{- end -}}
{{- if empty $actuation.nvidiaLockedGPUClock.runtimeClassName -}}
{{- fail "nodeAgent.actuation.nvidiaLockedGPUClock.runtimeClassName must not be empty" -}}
{{- end -}}
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
{{- if empty .Values.profiler.factSource.executionPoliciesKey -}}
{{- fail "profiler.factSource.executionPoliciesKey must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if empty .Values.profiler.factSource.actuationPoliciesKey -}}
{{- fail "profiler.factSource.actuationPoliciesKey must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- if or (eq .Values.profiler.factSource.executionFactsKey .Values.profiler.factSource.metricSourcesKey) (eq .Values.profiler.factSource.executionFactsKey .Values.profiler.factSource.workloadObservationKey) (eq .Values.profiler.factSource.executionFactsKey .Values.profiler.factSource.executionPoliciesKey) (eq .Values.profiler.factSource.metricSourcesKey .Values.profiler.factSource.workloadObservationKey) (eq .Values.profiler.factSource.metricSourcesKey .Values.profiler.factSource.executionPoliciesKey) (eq .Values.profiler.factSource.workloadObservationKey .Values.profiler.factSource.executionPoliciesKey) -}}
{{- fail "profiler.factSource keys must be different" -}}
{{- end -}}
{{- if has .Values.profiler.factSource.actuationPoliciesKey (list .Values.profiler.factSource.executionFactsKey .Values.profiler.factSource.metricSourcesKey .Values.profiler.factSource.workloadObservationKey .Values.profiler.factSource.executionPoliciesKey) -}}
{{- fail "profiler.factSource keys must be different" -}}
{{- end -}}
{{- if and (not .Values.profiler.workloadObserver.serviceAccount.create) (empty .Values.profiler.workloadObserver.serviceAccount.name) -}}
{{- fail "profiler.workloadObserver.serviceAccount.name is required when create=false" -}}
{{- end -}}
{{- if empty .Values.profiler.transitionObserver.observerNodeName -}}
{{- fail "profiler.transitionObserver.observerNodeName must not be empty when profiler.enabled=true" -}}
{{- end -}}
{{- $transitionObserverServiceAccount := .Values.profiler.transitionObserver.serviceAccount -}}
{{- if and (not $transitionObserverServiceAccount.create) (empty $transitionObserverServiceAccount.name) -}}
{{- fail "profiler.transitionObserver.serviceAccount.name is required when create=false" -}}
{{- end -}}
{{- if lt (int64 .Values.profiler.transitionObserver.activeDeadlineOverheadSeconds) 1 -}}
{{- fail "profiler.transitionObserver.activeDeadlineOverheadSeconds must be positive when profiler.enabled=true" -}}
{{- end -}}
{{- if lt (int64 .Values.profiler.transitionObserver.maximumObjectBytes) 1 -}}
{{- fail "profiler.transitionObserver.maximumObjectBytes must be positive when profiler.enabled=true" -}}
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
{{- if .Values.nodeAgent.enabled -}}
{{- if empty .Values.nodeAgent.image.repository -}}
{{- fail "nodeAgent.image.repository must not be empty when nodeAgent.enabled=true" -}}
{{- end -}}
{{- if empty $nodeAgentImageTag -}}
{{- fail "nodeAgent.image.tag or Chart.appVersion must not be empty when nodeAgent.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeAgent.image.pullPolicy -}}
{{- fail "nodeAgent.image.pullPolicy must not be empty when nodeAgent.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeAgent.updateStrategy.type -}}
{{- fail "nodeAgent.updateStrategy.type must not be empty when nodeAgent.enabled=true" -}}
{{- end -}}
{{- if and .Values.nodeAgent.excludeNodeNames .Values.nodeAgent.affinity.nodeAffinity -}}
{{- fail "nodeAgent.excludeNodeNames cannot be used together with nodeAgent.affinity.nodeAffinity" -}}
{{- end -}}
{{- if empty .Values.nodeAgent.kubernetesClient.tokenFile -}}
{{- fail "nodeAgent.kubernetesClient.tokenFile must not be empty when nodeAgent.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeAgent.kubernetesClient.caFile -}}
{{- fail "nodeAgent.kubernetesClient.caFile must not be empty when nodeAgent.enabled=true" -}}
{{- end -}}
{{- if and (eq .Values.nodeAgent.image.repository $nodeAgentPlaceholderRepository) (eq $nodeAgentImageTag $nodeAgentPlaceholderTag) (ne .Values.nodeAgent.image.pullPolicy "Never") -}}
{{- fail "nodeAgent image chill/node-agent:latest is a local development placeholder, not a published runtime image; set nodeAgent.image.repository/tag to a published image or use nodeAgent.image.pullPolicy=Never with a node-local image" -}}
{{- end -}}
{{- if empty .Values.nodeAgent.sources -}}
{{- fail "nodeAgent.sources must contain at least one source when nodeAgent.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeAgent.signatures -}}
{{- fail "nodeAgent.signatures must contain at least one signature when nodeAgent.enabled=true" -}}
{{- end -}}
{{- end -}}
{{- end -}}
