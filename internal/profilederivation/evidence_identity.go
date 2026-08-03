package profilederivation

import (
	"regexp"
	"strings"
)

var rawEvidenceDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validRawEvidenceIdentity(identity RawEvidenceIdentity) bool {
	return strings.TrimSpace(identity.Store) == identity.Store && identity.Store != "" &&
		strings.TrimSpace(identity.Key) == identity.Key && identity.Key != "" &&
		rawEvidenceDigestPattern.MatchString(identity.Digest) && identity.Size > 0
}

func rawEvidenceIdentityKey(identity RawEvidenceIdentity) string {
	return identity.Store + "\x00" + identity.Key + "\x00" + identity.Digest
}
