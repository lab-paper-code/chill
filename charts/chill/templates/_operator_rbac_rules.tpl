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
  - ""
  resources:
  - persistentvolumeclaims
  verbs:
  - create
  - get
  - list
  - patch
  - watch
- apiGroups:
  - ""
  resources:
  - secrets
  verbs:
  - create
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - services
  verbs:
  - create
  - delete
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
  - create
  - delete
  - get
  - list
  - patch
  - watch
- apiGroups:
  - batch
  resources:
  - jobs
  verbs:
  - create
  - delete
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
  verbs:
  - update
- apiGroups:
  - edge.dacs.io
  resources:
  - chillsystems/status
  verbs:
  - get
  - patch
  - update
- apiGroups:
  - edge.dacs.io
  resources:
  - deviceclasses
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - watch
{{- end -}}
