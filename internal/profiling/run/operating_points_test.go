package profilingrun

import (
	"math"
	"testing"
)

func TestOperatingPointsRequireExplicitPhaseAndSupplyInputs(t *testing.T) {
	valid := OperatingPointsContract{Utilizations: []float64{.2, .8}, SparseMeanGapMilliseconds: 1000,
		PhaseDurationSeconds: 30, PostLoadDurationSeconds: 30, RequestLifetimeSeconds: 10, DeliveryMarginMilliseconds: 100,
		FramedDecodedRequestUpperBytes: 1024}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	short := valid
	short.PhaseDurationSeconds = 1
	if err := short.Validate(); err != nil {
		t.Fatalf("positive phase input acquired an arbitrary duration minimum: %v", err)
	}
	if err := (*OperatingPointsContract)(nil).Validate(); err == nil {
		t.Fatal("accepted absent operating points")
	}
	for _, mutate := range []func(*OperatingPointsContract){
		func(p *OperatingPointsContract) { p.Utilizations = nil },
		func(p *OperatingPointsContract) { p.Utilizations = []float64{0} },
		func(p *OperatingPointsContract) { p.Utilizations = []float64{1} },
		func(p *OperatingPointsContract) { p.Utilizations = []float64{math.NaN()} },
		func(p *OperatingPointsContract) { p.SparseMeanGapMilliseconds = math.Inf(1) },
		func(p *OperatingPointsContract) { p.PhaseDurationSeconds = 0 },
		func(p *OperatingPointsContract) { p.PostLoadDurationSeconds = 0 },
		func(p *OperatingPointsContract) { p.PostLoadDurationSeconds = -1 },
		func(p *OperatingPointsContract) { p.RequestLifetimeSeconds = 0 },
		func(p *OperatingPointsContract) { p.DeliveryMarginMilliseconds = 0 },
		func(p *OperatingPointsContract) { p.FramedDecodedRequestUpperBytes = 0 },
		func(p *OperatingPointsContract) { p.FramedDecodedRequestUpperBytes = 64<<20 + 1 },
		func(p *OperatingPointsContract) { p.DeliveryMarginMilliseconds = p.RequestLifetimeSeconds * 1000 },
	} {
		p := valid
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted invalid operating points %+v", p)
		}
	}
}

func TestVerifiedRunFreezesPostLoadInputWithoutAliasing(t *testing.T) {
	config := validRunConfig()
	config.Measurement.OperatingPoints.PostLoadDurationSeconds = 17
	config.Measurement.DurationSeconds = 107
	verified, err := ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	config.Measurement.OperatingPoints.PostLoadDurationSeconds = 99
	record, err := verified.Record()
	if err != nil || record.Measurement.OperatingPoints.PostLoadDurationSeconds != 17 {
		t.Fatalf("source mutation changed frozen post-load input: %+v, %v", record, err)
	}
	record.Measurement.OperatingPoints.PostLoadDurationSeconds = 88
	record, err = verified.Record()
	if err != nil || record.Measurement.OperatingPoints.PostLoadDurationSeconds != 17 {
		t.Fatalf("returned record changed frozen post-load input: %+v, %v", record, err)
	}
}
