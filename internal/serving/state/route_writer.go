package state

import (
	"slices"
	"time"
)

// RouteWriters records each destination's latest admitted write, not its receipt.
// Entries survive physical reservation return and receipt collection.
type RouteWriters map[string]time.Time

func (writers RouteWriters) Protects(admitted time.Time, instance string) bool {
	return writers[instance].After(admitted)
}

// RouteWriterDestinations excludes borrowed physical resources: UID-wide route
// context does not transfer another active owner's destination authority.
func RouteWriterDestinations(owned, borrowed map[string]bool) []string {
	var destinations []string
	for id := range owned {
		if !borrowed[id] {
			destinations = append(destinations, id)
		}
	}
	slices.Sort(destinations)
	return destinations
}
