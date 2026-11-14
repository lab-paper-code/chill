package serving

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func TestProfileBundleMillionRecords(t *testing.T) {
	const count = 150000
	p := Profile{ID: "namespace/measurement/00000000-0000-0000-0000-000000000000"}
	var slope derivation.SessionParameter
	if err := json.Unmarshal([]byte(`{"status":"Identified","value":0.2}`), &slope); err != nil {
		t.Fatal(err)
	}
	result := derivation.SessionResult{SchemaVersion: derivation.SessionResultSchemaVersion}
	for b := 1; b <= 128; b *= 2 {
		services, gaps := make([]int64, count), make([]int64, count)
		samples := make([]firstfloor.ServiceSample, count)
		for i := range services {
			services[i], gaps[i] = 1000000+int64(i%1000), int64(i%3)*1000000
			samples[i] = firstfloor.ServiceSample{ServiceDurationNanos: services[i], PrecedingIdleNanos: &gaps[i], TrialID: "00000000-0000-0000-0000-000000000000"}
		}
		d, err := firstfloor.NewServiceDistribution(samples)
		if err != nil {
			t.Fatal(err)
		}
		p.Batches = append(p.Batches, BatchProfile{Batch: b, ServiceSamples: samples, ServiceDistribution: d, InputSlopeJoules: &slope})
		result.Batches = append(result.Batches, derivation.SessionBatch{Batch: b, ServiceNanos: services, PrecedingIdleNanos: gaps})
	}
	raw, err := json.Marshal(p.NativeBundle())
	if err != nil {
		t.Fatal(err)
	}
	rawResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bundle=%d bytes, %.3f bytes/record; result=%d bytes", len(raw), float64(len(raw))/(8*count), len(rawResult))
	if len(raw) >= firstfloor.MaxFrameBytes || len(raw) > len(rawResult) {
		t.Fatalf("bundle %d must fit native cap %d and session result %d", len(raw), firstfloor.MaxFrameBytes, len(rawResult))
	}
	var decoded firstfloor.ProfileBundle
	directory := t.TempDir()
	profilePath, configPath := filepath.Join(directory, "bundle.json"), filepath.Join(directory, "config.json")
	if err := os.WriteFile(profilePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	config := firstfloor.Config{
		Model: "model", Mode: "serving", Inputs: []string{"x"}, Outputs: []string{"y"},
		RunnerCommand: []string{"runner"}, RunnerTimeoutMS: 1000,
		MaxQueueRequests: 128, MaxQueueBytes: 1 << 20, MaxRequestBytes: 1 << 20,
		MaxBatchSize: 128, ProfilePath: profilePath,
		Policy: firstfloor.Policy{ProfileID: p.ID, DecisionID: "decision"},
	}
	for _, batch := range p.Batches {
		config.BatchSizes = append(config.BatchSizes, batch.Batch)
	}
	configRaw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configRaw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := firstfloor.ReadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for i, batch := range decoded.Batches {
		encoded, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Service []int64 `json:"serviceNanos"`
			Gaps    []int64 `json:"precedingIdleNanos"`
		}
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if len(wire.Service) != count || len(wire.Gaps) != count {
			t.Fatalf("lost records: %d/%d", len(wire.Service), len(wire.Gaps))
		}
		d := loaded.Policy.Batches[i].Distribution
		if d.RecordCount() != count {
			t.Fatalf("native lost records: %d", d.RecordCount())
		}
		records := make(map[[2]int64]int)
		for _, row := range p.Batches[i].ServiceSamples {
			records[[2]int64{row.ServiceDurationNanos, *row.PrecedingIdleNanos}]++
		}
		for j := range wire.Service {
			records[[2]int64{wire.Service[j], wire.Gaps[j]}]--
		}
		for pair, remaining := range records {
			if remaining != 0 {
				t.Fatalf("record %v changed count by %d", pair, remaining)
			}
		}
		for _, percentile := range []float64{50, 95, 99} {
			for _, gap := range []int64{0, 1000000, 2000000} {
				want, support, ok := p.Batches[i].ServiceDistribution.QuantileNanos(&gap, percentile)
				got, gotSupport, gotOK := d.QuantileNanos(&gap, percentile)
				if want != got || support != gotSupport || ok != gotOK {
					t.Fatalf("batch %d quantile changed", batch.Size)
				}
			}
		}
	}
}
