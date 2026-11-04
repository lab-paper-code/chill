package state

import "time"

// RouteWriters records each destination's latest admitted write, not its receipt.
// Entries survive physical reservation return and receipt collection.
type RouteWriters map[string]time.Time

func (writers RouteWriters) Protects(admitted time.Time, instance string) bool {
	return writers[instance].After(admitted)
}
