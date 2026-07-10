{{- define "chill.validateValues" -}}
{{- $nodeDiscoveryPlaceholderRepository := "chill/node-discovery" -}}
{{- $nodeDiscoveryPlaceholderTag := "latest" -}}
{{- $nodeDiscoveryImageTag := include "chill.nodeDiscoveryImageTag" . -}}
{{- $operatorImageTag := include "chill.operatorImageTag" . -}}
{{- if empty (include "chill.systemName" .) -}}
{{- fail "system.name or release name must not be empty" -}}
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
{{- if and (not .Values.operator.serviceAccount.create) (empty .Values.operator.serviceAccount.name) -}}
{{- fail "operator.serviceAccount.name must be set when operator.serviceAccount.create=false" -}}
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
{{- if and .Values.discovery.enabled .Values.discovery.requireCatalogMatch .Values.discovery.catalog.enabled (empty .Values.discovery.catalog.classes) -}}
{{- fail "discovery.catalog.classes must contain at least one class when discovery.requireCatalogMatch=true" -}}
{{- end -}}
{{- if .Values.nodeDiscovery.enabled -}}
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
