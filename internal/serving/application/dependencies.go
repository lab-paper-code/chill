package application

import (
	"time"

	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/client-go/kubernetes"
)

type Options struct {
	ActuationPoliciesKey   string
	FactsConfigMap         string
	FactsNamespace         string
	LoadGenerator          servingkube.LoadGeneratorConfig
	MaxEvidenceBytes       int64
	MaxRequestBytes        int64
	MetricSourcesKey       string
	OperationTimeout       time.Duration
	ProfileNamespaces      []string
	RequestTimeout         time.Duration
	SchedulerConfigKey     string
	SchedulerConfigMap     string
	ServingNamespace       string
	StoreBucket            string
	StoreCredentialsSecret string
	StoreEndpoint          string
	StoreName              string
	StoreRegion            string
}

type Live struct {
	Decision    func(serving.SchedulerDecision, serving.SchedulerSnapshot)
	Action      func(serving.SchedulerActionObservation, time.Time)
	PowerScope  func([]servingkube.MeterDeclaration, bool)
	MeterSource func(metricobserver.Source, string) metricobserver.Source
}

type Dependencies struct {
	Cluster    *servingkube.ClusterState
	Operations *servingkube.Operations
	Store      *servingkube.Store
	Kube       kubernetes.Interface
	// SettingsKube preserves the HTTP mutation client's budget independently of control refreshes.
	SettingsKube          kubernetes.Interface
	Execution             *serving.Runtime
	Observer              *servingkube.Observer
	Objects               *stores3.Store
	ObservationLog        *serving.ObservationLog
	Options               Options
	GatewayRequestTimeout time.Duration
	Live                  *Live
	Catalog               *servingkube.Catalog
	Costs                 *servingkube.CostEvidence
}
