{{- define "chill.validateValues" -}}
{{- $controllerPlaceholderRepository := "chill/controller" -}}
{{- $controllerPlaceholderTag := "latest" -}}
{{- $nodeDiscoveryPlaceholderRepository := "chill/node-discovery" -}}
{{- $nodeDiscoveryPlaceholderTag := "latest" -}}
{{- $controllerImageTag := include "chill.controllerImageTag" . -}}
{{- $nodeDiscoveryImageTag := include "chill.nodeDiscoveryImageTag" . -}}
{{- if empty .Values.controller.image.repository -}}
{{- fail "controller.image.repository must not be empty" -}}
{{- end -}}
{{- if empty $controllerImageTag -}}
{{- fail "controller.image.tag or Chart.appVersion must not be empty" -}}
{{- end -}}
{{- if empty .Values.controller.image.pullPolicy -}}
{{- fail "controller.image.pullPolicy must not be empty" -}}
{{- end -}}
{{- if gt (int .Values.controller.replicaCount) 0 -}}
{{- if and (eq .Values.controller.image.repository $controllerPlaceholderRepository) (eq $controllerImageTag $controllerPlaceholderTag) (ne .Values.controller.image.pullPolicy "Never") -}}
{{- fail "controller image chill/controller:latest is a local development placeholder, not a published runtime image; set controller.image.repository/tag to a published image or use controller.image.pullPolicy=Never with a node-local image" -}}
{{- end -}}
{{- end -}}
{{- if and .Values.discovery.enabled .Values.discovery.requireCatalogMatch .Values.discovery.catalog.enabled (empty .Values.discovery.catalog.classes) -}}
{{- fail "discovery.catalog.classes must contain at least one class when discovery.requireCatalogMatch=true" -}}
{{- end -}}
{{- if and .Values.discovery.catalog.enabled .Values.discovery.catalog.createDeviceClasses (empty .Values.discovery.catalog.classes) -}}
{{- fail "discovery.catalog.classes must contain at least one class when discovery.catalog.createDeviceClasses=true" -}}
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
