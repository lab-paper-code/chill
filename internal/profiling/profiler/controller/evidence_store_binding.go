package controller

import storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"

// EvidenceStoreBinding is the verified delivery contract consumed by Run
// execution and evidence finalization.
type EvidenceStoreBinding = storekube.Binding
