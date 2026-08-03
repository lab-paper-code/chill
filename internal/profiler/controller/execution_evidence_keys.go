package controller

import (
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

func rawEvidenceObjectKey(uid types.UID, digest string) string {
	return evidenceObjectKey(uid, "raw", digest)
}

func finalizedEvidenceObjectKey(uid types.UID, digest string) string {
	return evidenceObjectKey(uid, "execution", digest)
}

func evidenceObjectKey(uid types.UID, kind, digest string) string {
	return "profiling-runs/" + string(uid) + "/" + kind + "/" + strings.TrimPrefix(digest, "sha256:") + ".json"
}
