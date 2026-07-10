{{- define "chill.operatorRoleRules" -}}
- apiGroups:
  - ""
  resources:
  - configmaps
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - nodes
  verbs:
  - get
  - list
  - patch
  - watch
- apiGroups:
  - apps
  resources:
  - daemonsets
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - apps
  resources:
  - deployments
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - edge.dacs.io
  resources:
  - chillsystems
  verbs:
  - get
  - list
  - patch
  - watch
- apiGroups:
  - edge.dacs.io
  resources:
  - chillsystems/finalizers
  - clusterenergymodels/finalizers
  - deviceclasses/finalizers
  - deviceprofiles/finalizers
  - modelspecs/finalizers
  verbs:
  - update
- apiGroups:
  - edge.dacs.io
  resources:
  - chillsystems/status
  - clusterenergymodels/status
  - deviceclasses/status
  - deviceprofiles/status
  - modelspecs/status
  verbs:
  - get
  - patch
  - update
- apiGroups:
  - edge.dacs.io
  resources:
  - clusterenergymodels
  - deviceclasses
  - deviceprofiles
  - modelspecs
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
{{- end -}}
