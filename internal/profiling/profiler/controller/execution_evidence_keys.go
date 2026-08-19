package controller

import "k8s.io/apimachinery/pkg/types"

func rawEvidenceObjectKey(uid types.UID) string {
	return evidenceObjectKey(uid, "raw")
}

func finalizedEvidenceObjectKey(uid types.UID) string {
	return evidenceObjectKey(uid, "execution")
}

func evidenceObjectKey(uid types.UID, kind string) string {
	return "profiling-runs/" + string(uid) + "/" + kind + ".json"
}
