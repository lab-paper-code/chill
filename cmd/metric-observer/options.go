package main

import (
	"errors"
	"flag"
	"strings"
	"time"
)

const (
	defaultReadyPath         = "/coordination/observer-ready"
	defaultStartSignalPath   = "/coordination/measurement-start"
	defaultEndSignalPath     = "/coordination/measurement-end"
	defaultMetricResultPath  = "/coordination/metric-result.json"
	defaultRuntimeFailedPath = "/coordination/runtime-failed"
)

type options struct {
	runConfig         string
	readyPath         string
	startPath         string
	endPath           string
	metricResultPath  string
	startWaitTimeout  time.Duration
	endWaitTimeout    time.Duration
	maxEvents         int
	maxResultBytes    int64
	runtimeFailedPath string
}

func (o *options) bindFlags(set *flag.FlagSet) {
	set.StringVar(&o.runConfig, "run-config", "", "exact RunConfig generated for this Job")
	set.StringVar(&o.readyPath, "ready-file", defaultReadyPath, "observer-ready signal path")
	set.StringVar(&o.startPath, "start-signal-file", defaultStartSignalPath, "measurement-start signal path")
	set.StringVar(&o.endPath, "end-signal-file", defaultEndSignalPath, "measurement-end signal path")
	set.StringVar(&o.metricResultPath, "result-file", defaultMetricResultPath, "atomic MetricObserver result path")
	set.StringVar(&o.runtimeFailedPath, "runtime-failed-file", defaultRuntimeFailedPath, "runtime-failed signal path")
	set.DurationVar(&o.startWaitTimeout, "start-wait-timeout", 0, "Job-derived maximum wait for measurement-start")
	set.DurationVar(&o.endWaitTimeout, "end-wait-timeout", 0, "Job-derived maximum wait for measurement-end")
	set.IntVar(&o.maxEvents, "max-events", 0, "Job-derived maximum retained sample and failure events")
	set.Int64Var(&o.maxResultBytes, "max-result-bytes", 0, "Job-derived maximum serialized metric result bytes")
}

func (o options) validate() error {
	if strings.TrimSpace(o.runConfig) == "" {
		return errors.New("RunConfig is required")
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
	if o.maxResultBytes < 1 {
		return errors.New("maximum result bytes must be supplied by the Job execution contract")
	}
	return nil
}
