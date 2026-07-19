package component

import (
	"crypto/sha256"
	"fmt"
)

const EvidenceStoreConfigDigestAnnotation = "edge.dacs.io/evidence-store-config-digest"

// EvidenceStoreConfigDigest identifies the exact operator-owned configuration
// bytes whose storage bootstrap has completed.
func EvidenceStoreConfigDigest(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("sha256:%x", digest)
}
