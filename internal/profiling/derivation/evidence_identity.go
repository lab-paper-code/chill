package profilederivation

import (
	"strings"
)

func validRawEvidenceIdentity(identity RawEvidenceIdentity) bool {
	return strings.TrimSpace(identity.Store) == identity.Store && identity.Store != "" &&
		strings.TrimSpace(identity.Key) == identity.Key && identity.Key != "" &&
		identity.Size > 0
}

func rawEvidenceIdentityKey(identity RawEvidenceIdentity) string {
	return identity.Store + "\x00" + identity.Key
}
