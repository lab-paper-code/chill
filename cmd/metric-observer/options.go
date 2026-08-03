package main

import (
	"errors"
	"flag"
	"regexp"
	"time"
)

const (
	defaultIntentPath             = "/etc/chill/run/intent.json"
	defaultReadyPath              = "/coordination/observer-ready"
	defaultStartSignalPath        = "/coordination/measurement-start"
	defaultEndSignalPath          = "/coordination/measurement-end"
	defaultMetricResultPath       = "/coordination/metric-result.json"
	maximumIntentBytes      int64 = 1 << 20
	maximumResultBytes      int64 = 8 << 20
)

var canonicalDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type options struct {
	intentPath       string
	intentDigest     string
	readyPath        string
	startPath        string
	endPath          string
	metricResultPath string
	startWaitTimeout time.Duration
	endWaitTimeout   time.Duration
	maxEvents        int
}

func (o *options) bindFlags(set *flag.FlagSet) {
	set.StringVar(&o.intentPath, "intent-file", defaultIntentPath, "read-only frozen Run intent JSON")
	set.StringVar(&o.intentDigest, "intent-digest", "", "expected frozen Run intent SHA-256")
	set.StringVar(&o.readyPath, "ready-file", defaultReadyPath, "observer-ready signal path")
	set.StringVar(&o.startPath, "start-signal-file", defaultStartSignalPath, "measurement-start signal path")
	set.StringVar(&o.endPath, "end-signal-file", defaultEndSignalPath, "measurement-end signal path")
	set.StringVar(&o.metricResultPath, "result-file", defaultMetricResultPath, "atomic MetricObserver result path")
	set.DurationVar(&o.startWaitTimeout, "start-wait-timeout", 0, "Job-derived maximum wait for measurement-start")
	set.DurationVar(&o.endWaitTimeout, "end-wait-timeout", 0, "Job-derived maximum wait for measurement-end")
	set.IntVar(&o.maxEvents, "max-events", 0, "Job-derived maximum retained sample and failure events")
}

func (o options) validate() error {
	if !canonicalDigest.MatchString(o.intentDigest) {
		return errors.New("intent digest must be a canonical SHA-256")
	}
	if o.startWaitTimeout <= 0 {
		return errors.New("start wait timeout must be supplied by the Job execution contract")
	}
	if o.endWaitTimeout <= 0 {
		return errors.New("end wait timeout must be supplied by the Job execution contract")
	}
	if o.maxEvents < 1 {
		return errors.New("maximum retained events must be supplied by the Job execution contract")
	}
	return nil
}
