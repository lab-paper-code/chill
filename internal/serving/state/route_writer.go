package state

import (
	"slices"
	"time"
)

// RouteWriter records an admitted write, not a successful receipt. Its scope is
// immutable and survives physical reservation return and receipt collection.
type RouteWriter struct {
	AdmittedAt time.Time
	Instances  []string
}

func (writer RouteWriter) Protects(admitted time.Time, instance string) bool {
	return writer.AdmittedAt.After(admitted) && slices.Contains(writer.Instances, instance)
}
