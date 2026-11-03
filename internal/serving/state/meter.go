package state

import (
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
)

// MeterDeclaration is explicit evidence scope, never inferred from an URL.
type MeterDeclaration struct {
	ID     string                        `json:"id"`
	Node   serving.NodeReference         `json:"node"`
	Scope  string                        `json:"scope"`
	Source metricobserver.SourceIdentity `json:"source"`
}
