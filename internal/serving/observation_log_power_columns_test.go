package serving

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestOperatingPowerColumnsRoundTripAndSize(t *testing.T) {
	from := time.Date(2026, 11, 3, 3, 21, 55, 245000000, time.UTC)
	to := from.Add(15 * time.Minute)
	bucket := 5 * time.Second
	count, err := PowerBucketCount(from, to, bucket)
	if err != nil {
		t.Fatal(err)
	}
	series := OperatingPowerSeries{From: from, To: to, BucketNanos: bucket, Buckets: make([]OperatingPowerWindow, count)}
	start := from.Truncate(bucket)
	for i := range series.Buckets {
		left := start.Add(time.Duration(i) * bucket)
		w := OperatingPowerWindow{From: maxTime(from, left), To: minTime(to, left.Add(bucket)), Scope: "whole-pool-wall", Complete: true, Reasons: []string{}, Nodes: make([]OperatingNodePower, 10)}
		w.Coverage = ObservationCoverage{From: &from, To: &to, Sources: []ObservationSource{{From: from, To: to, Stream: "scheduler", Source: "local"}}, Gaps: []ObservationGap{}}
		for j := range w.Nodes {
			joules, watts, samples := 1094.951849909732+float64(i+j), 218.99036998194637+float64(j), 7
			first, last := w.From.Add(123456789*time.Nanosecond), w.To.Add(-123456789*time.Nanosecond)
			w.Nodes[j] = OperatingNodePower{Node: NodeReference{Name: fmt.Sprintf("physical-node-%d", j), UID: fmt.Sprintf("1d8b2d96-a51a-40c1-b020-%012d", j)}, Meter: "shellyplugsg3-e4b063ddacc0", Scope: "node-exclusive-wall", Joules: &joules, MeanWatts: &watts, Samples: 9, MaximumSampleGapSeconds: 0.761647874, SourceTimestampAbsent: true, ObservedSamples: &samples, FirstObservedAt: &first, LastObservedAt: &last, Reasons: []string{}}
		}
		w.WholePoolJoules, w.WholePoolMeanWatts = w.Nodes[0].Joules, w.Nodes[0].MeanWatts
		w.ActivePoolJoules, w.ActivePoolMeanWatts = w.Nodes[1].Joules, w.Nodes[1].MeanWatts
		series.Buckets[i] = w
	}
	// Include missing, measured zero, failures, timestamp absence, coverage gaps,
	// null vs empty slices, and different node counts/order in the round trip.
	zero, none := 0.0, 0
	series.Buckets[1].Complete = false
	series.Buckets[1].WholePoolJoules = nil
	series.Buckets[1].Reasons = []string{"no observed samples in bucket"}
	series.Buckets[1].GapUntil = &to
	series.Buckets[1].Coverage.Gaps = []ObservationGap{{From: from, To: to, Stream: "scheduler", Reason: "StreamUnavailable"}}
	series.Buckets[1].Nodes[0] = OperatingNodePower{Node: NodeReference{UID: "missing"}, ObservedSamples: &none, Reasons: []string{"missing source"}, ReadFailures: 3}
	series.Buckets[2].Nodes = []OperatingNodePower{{Joules: &zero, MeanWatts: &zero}}
	series.Buckets[3].Nodes = nil
	series.Buckets[4].Nodes = []OperatingNodePower{}
	ordinary, err := json.Marshal(series)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(series.Columns())
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) >= 1000000 || len(compact) >= len(ordinary) {
		t.Fatalf("15m/5s/%d buckets/10 nodes: columns=%d ordinary=%d", count, len(compact), len(ordinary))
	}
	t.Logf("15m/5s/%d buckets/10 nodes: columns=%d ordinary=%d bytes", count, len(compact), len(ordinary))
	var wire struct {
		From        time.Time                    `json:"from"`
		To          time.Time                    `json:"to"`
		BucketNanos time.Duration                `json:"bucketNanos"`
		Windows     map[string][]json.RawMessage `json:"windows"`
		Nodes       map[string][]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(compact, &wire); err != nil {
		t.Fatal(err)
	}
	restored := make([]map[string]json.RawMessage, count)
	for i := range restored {
		row := map[string]json.RawMessage{}
		left := wire.From.UTC().Truncate(wire.BucketNanos).Add(time.Duration(i) * wire.BucketNanos)
		row["from"], _ = json.Marshal(maxTime(wire.From, left))
		row["to"], _ = json.Marshal(minTime(wire.To, left.Add(wire.BucketNanos)))
		for key, values := range wire.Windows {
			row[key] = values[i]
		}
		const jsonNull = "null"
		if string(row["gapUntil"]) == jsonNull {
			delete(row, "gapUntil")
		}
		var identities []json.RawMessage
		if err := json.Unmarshal(wire.Nodes["node"][i], &identities); err != nil {
			t.Fatal(err)
		}
		var nodes []map[string]json.RawMessage
		if identities != nil {
			nodes = make([]map[string]json.RawMessage, len(identities))
		}
		for j := range nodes {
			nodes[j] = map[string]json.RawMessage{}
		}
		for key, buckets := range wire.Nodes {
			var values []json.RawMessage
			if err := json.Unmarshal(buckets[i], &values); err != nil {
				t.Fatal(err)
			}
			if len(values) != len(nodes) {
				t.Fatalf("column length mismatch: %s", key)
			}
			for j, value := range values {
				if string(value) == jsonNull && (key == "observedSamples" || key == "firstObservedAt" || key == "lastObservedAt") {
					continue
				}
				nodes[j][key] = value
			}
		}
		row["nodes"], _ = json.Marshal(nodes)
		restored[i] = row
	}
	decoded, err := json.Marshal(map[string]any{"from": wire.From, "to": wire.To, "bucketNanos": wire.BucketNanos, "buckets": restored})
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(ordinary, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("column round trip changed bucket information")
	}
}
