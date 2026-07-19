package component

import (
	"regexp"
	"testing"
)

func TestEvidenceStoreConfigDigestIsCanonicalAndContentBound(t *testing.T) {
	first := EvidenceStoreConfigDigest("bucket: first")
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("digest = %q", first)
	}
	if first != EvidenceStoreConfigDigest("bucket: first") {
		t.Fatal("identical configuration bytes produced different digests")
	}
	if first == EvidenceStoreConfigDigest("bucket: second") {
		t.Fatal("different configuration bytes produced the same digest")
	}
}
