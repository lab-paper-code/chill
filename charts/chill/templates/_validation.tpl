{{- define "chill.validateValues" -}}
{{- if empty .Values.controller.image.repository -}}
{{- fail "controller.image.repository must not be empty" -}}
{{- end -}}
{{- if empty .Values.controller.image.tag -}}
{{- fail "controller.image.tag must not be empty" -}}
{{- end -}}
{{- if empty .Values.controller.image.pullPolicy -}}
{{- fail "controller.image.pullPolicy must not be empty" -}}
{{- end -}}
{{- if and .Values.discovery.enabled .Values.discovery.requireCatalogMatch .Values.discovery.catalog.enabled (empty .Values.discovery.catalog.classes) -}}
{{- fail "discovery.catalog.classes must contain at least one class when discovery.requireCatalogMatch=true" -}}
{{- end -}}
{{- if .Values.nodeDiscovery.enabled -}}
{{- if empty .Values.nodeDiscovery.image.repository -}}
{{- fail "nodeDiscovery.image.repository must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.image.tag -}}
{{- fail "nodeDiscovery.image.tag must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.image.pullPolicy -}}
{{- fail "nodeDiscovery.image.pullPolicy must not be empty when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.sources -}}
{{- fail "nodeDiscovery.sources must contain at least one source when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- if empty .Values.nodeDiscovery.signatures -}}
{{- fail "nodeDiscovery.signatures must contain at least one signature when nodeDiscovery.enabled=true" -}}
{{- end -}}
{{- end -}}
{{- end -}}
