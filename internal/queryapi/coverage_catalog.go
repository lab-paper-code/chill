package queryapi

import (
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"k8s.io/apimachinery/pkg/types"
)

type CoverageQuery struct {
	Batches []int
	Node    string
	Summary bool
}

// CoverageProfile carries only fields consulted by measurement queries. ReusePayload
// remains opaque so result rows retain the original serving wire representation.
type CoverageProfile struct {
	ID           string
	Reference    evidencestore.ObjectIdentity
	Condition    profilederivation.CurvePlanSpec
	NodeName     string
	MeasuredAt   time.Time
	Batches      []int
	Reuse        *CoverageReuseIdentity
	ReusePayload any
}

type CoverageReuseIdentity struct {
	SourceProfileID       string
	ConfirmationProfileID string
	Outcome               string
}

type CoverageCatalogIssue struct {
	Source types.NamespacedName
	Code   string
	Reason string
}

type CoverageCatalog struct {
	ConfigVersion       string
	Revision            string
	SourceError         string
	Profiles            []CoverageProfile
	CurrentMeasurements []profilederivation.MeasurementSelection
	MeasurementFacts    map[string]profilederivation.MeasurementFacts
	Insufficiencies     []CoverageCatalogIssue
}

type CoverageCatalogRead struct {
	Reporting         CoverageCatalog
	SourceCheckedAt   time.Time
	Accepted          *CoverageCatalog
	AcceptedOccupancy []CoverageSelectedOccupancy
	ObservedOccupancy []OccupancyEstimate
}

type CoverageSelectedOccupancy struct {
	Node          MeasurementIdentity
	Model         string
	PowerMode     string
	ExecutionPath string
	ProfileID     string
	Batch         int
	Provenance    *SchedulerOccupancy
}

// CoverageDataset and CoveragePreprocessor retain only the native input-contract
// fields consulted when matching a frozen measured condition.
type CoverageDataset struct {
	Name   string
	UID    string
	Fields []api.RawField
}

type CoveragePreprocessor struct {
	Name      string
	Fields    []api.RawField
	Artifacts []CoveragePreprocessorArtifact
}

type CoveragePreprocessorArtifact struct {
	Model    string
	Artifact string
	Image    string
	Path     string
}

// CoverageBase is an immutable successful-source snapshot. Refresh ownership and
// retention belong to kube.CoverageService; projection only reads these facts.
type CoverageBase struct {
	Inventory                    InventoryView
	Compatibility                ModelCompatibilityMatrixView
	Requests                     api.BatchEnergyOptimizationList
	Runs                         api.ProfilingRunList
	Occupancies                  api.DispatchOccupancyMeasurementList
	OccupancyResults             map[string]OccupancyEvidenceProjection
	Catalog                      CoverageCatalog
	Datasets                     []CoverageDataset
	Preprocessors                []CoveragePreprocessor
	DatasetsResourceVersion      string
	PreprocessorsResourceVersion string
	Adopted                      map[string]bool
	AcceptedOccupancy            []CoverageSelectedOccupancy // nil is unavailable; empty confirms no selection.
	ObservedOccupancy            []OccupancyEstimate
	Checked                      map[string]time.Time
	Failures                     map[string]error
}

const (
	CoverageCatalogSource  = "catalog"
	CoverageFreshnessStale = "stale"
)

var CoverageSourceKinds = [...]string{
	"inventory", "compatibility", CoverageCatalogSource, "BEO", "Run", "DOM", "input-contract",
}

func (b *CoverageBase) Oldest() time.Time {
	var oldest time.Time
	for _, at := range b.Checked {
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	return oldest
}

type MeasurementError struct {
	Code    string
	Message string
}

func (e *MeasurementError) Error() string { return e.Message }
