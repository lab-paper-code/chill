{{- define "chill.dashboardReadRules" -}}
{{- $servingNamespace := default .Release.Namespace .Values.servingEntrypoint.namespace -}}
{{- $profileNamespaces := default (list $servingNamespace) .Values.servingEntrypoint.profileNamespaces -}}
  # Relay GETs: definitions, live, observations, evaluations, scheduler and Query views.
  # Deliberately no raw observations, scheduler mutations or evaluation mutations.
  # catalog-read is Query's owner-only upstream read, not a dashboard reader route.
  - nonResourceURLs: ["/chill/api/v1/live"]
    verbs: [get]
  - nonResourceURLs: ["/chill/api/v1/models", "/chill/api/v1/models/*", "/chill/api/v1/preprocessors", "/chill/api/v1/preprocessors/*", "/chill/api/v1/datasets", "/chill/api/v1/datasets/*", "/chill/api/v1/workloads", "/chill/api/v1/workloads/*"]
    verbs: [get]
  - nonResourceURLs: ["/chill/api/v1/pools", "/chill/api/v1/pools/*", "/chill/api/v1/observations/workloads/*", "/chill/api/v1/observations/load", "/chill/api/v1/observations/scheduler", "/chill/api/v1/observations/power", "/chill/api/v1/observations/events", "/chill/api/v1/observations/clients"]
    verbs: [get]
  - nonResourceURLs: ["/chill/api/v1/observations/occupancy"]
    verbs: [get]
  - nonResourceURLs: ["/chill/api/v1/experiments", "/chill/api/v1/experiments/*", "/chill/api/v1/evaluations", "/chill/api/v1/evaluations/*"]
    verbs: [get]
  - nonResourceURLs: ["/chill/api/v1/systems", "/chill/api/v1/inventory", "/chill/api/v1/model-compatibility", "/chill/api/v1/model-compatibility-matrix"]
    verbs: [get]
  - nonResourceURLs: ["/chill/api/v1/scheduler", "/chill/api/v1/scheduler/settings", "/chill/api/v1/scheduler/state", "/chill/api/v1/scheduler/profiles", "/chill/api/v1/scheduler/capacity"]
    verbs: [get]
  {{- range $namespace := $profileNamespaces | uniq | sortAlpha }}
  - nonResourceURLs:
      - {{ printf "/chill/api/v1/namespaces/%s/measurement-coverage" $namespace | quote }}
      - {{ printf "/chill/api/v1/namespaces/%s/batch-energy-optimizations" $namespace | quote }}
      - {{ printf "/chill/api/v1/namespaces/%s/batch-energy-optimizations/*" $namespace | quote }}
      - {{ printf "/chill/api/v1/namespaces/%s/profiling-runs" $namespace | quote }}
      - {{ printf "/chill/api/v1/namespaces/%s/profiling-runs/*" $namespace | quote }}
      - {{ printf "/chill/api/v1/namespaces/%s/operation-cost-measurements" $namespace | quote }}
      - {{ printf "/chill/api/v1/namespaces/%s/operation-cost-measurements/*" $namespace | quote }}
      - {{ printf "/chill/api/v1/namespaces/%s/dispatch-occupancy-measurements" $namespace | quote }}
      - {{ printf "/chill/api/v1/namespaces/%s/dispatch-occupancy-measurements/*" $namespace | quote }}
    verbs: [get]
  {{- end }}
{{- end -}}
