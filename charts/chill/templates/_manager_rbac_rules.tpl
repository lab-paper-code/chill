{{- define "chill.managerRoleRules" -}}
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
  - update
  - watch
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
- apiGroups:
  - edge.dacs.io
  resources:
  - clusterenergymodels/finalizers
  - deviceclasses/finalizers
  - deviceprofiles/finalizers
  - modelspecs/finalizers
  verbs:
  - update
- apiGroups:
  - edge.dacs.io
  resources:
  - clusterenergymodels/status
  - deviceclasses/status
  - deviceprofiles/status
  - modelspecs/status
  verbs:
  - get
  - patch
  - update
{{- end -}}
