package main

import (
	"errors"
	"flag"
	"regexp"
)

const (
	defaultIntentPath            = "/etc/chill/run/intent.json"
	defaultReadyPath             = "/coordination/observer-ready"
	defaultStartSignalPath       = "/coordination/measurement-start"
	defaultPowerResultPath       = "/coordination/power-result.json"
	maximumIntentBytes     int64 = 1 << 20
	maximumResultBytes     int64 = 8 << 20
)

var canonicalDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type options struct {
	intentPath      string
	intentDigest    string
	readyPath       string
	startPath       string
	powerResultPath string
}

func (o *options) bindFlags(set *flag.FlagSet) {
	set.StringVar(&o.intentPath, "intent-file", defaultIntentPath, "read-only frozen Run intent JSON")
	set.StringVar(&o.intentDigest, "intent-digest", "", "expected frozen Run intent SHA-256")
	set.StringVar(&o.readyPath, "ready-file", defaultReadyPath, "observer-ready signal path")
	set.StringVar(&o.startPath, "start-signal-file", defaultStartSignalPath, "measurement-start signal path")
	set.StringVar(&o.powerResultPath, "result-file", defaultPowerResultPath, "atomic PowerObserver result path")
}

func (o options) validate() error {
	if !canonicalDigest.MatchString(o.intentDigest) {
		return errors.New("intent digest must be a canonical SHA-256")
	}
	return nil
}
