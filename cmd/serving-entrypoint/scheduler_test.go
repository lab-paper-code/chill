package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestSchedulerConfigurationRequiresActualEvidenceAndInventory(t *testing.T) {
	settings := serving.SchedulerSettings{
		ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second,
		MaxObservationAge: time.Second, Replays: 1, MaxCandidates: 1, MaxReplicas: 1,
		MaxInstances: 1, MaxReplayRequests: 1, MaxCPUs: 2,
	}
	rawSettings, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{name: "explicit controls-off configuration", fields: `"cpuInventory":{"node":[0,2]},` +
			`"gpuInventory":{"node":["GPU-real"]},"reserved":{"node":{"cpus":[2]}}`, valid: true},
		{name: "duplicate physical CPU", fields: `"cpuInventory":{"node":[0,0]}`},
		{name: "negative physical CPU", fields: `"cpuInventory":{"node":[-1]}`},
		{name: "unobserved reserved CPU", fields: `"cpuInventory":{"node":[0]},"reserved":{"node":{"cpus":[2]}}`},
		{name: "duplicate GPU identity", fields: `"gpuInventory":{"node":["GPU-real","GPU-real"]}`},
		{name: "fake profile injection", fields: `"preparations":{"p":{"instance":{"profile":{"id":"fake"}}}}`},
		{name: "fake cost injection", fields: `"costs":[{"id":"fake","trials":[{"joules":0}]}]`},
		{name: "invalid meter scope", fields: `"meters":[{"id":"wall","node":{"name":"node","uid":"uid"},` +
			`"scope":"inferred","source":{"nodeName":"node"}}]`},
		{name: "invalid power polling", fields: `"powerObservation":{"sampling":{"Interval":0,` +
			`"Duration":1000000000,"RequestTimeout":1,"MaxEvents":10},"coverage":` +
			`{"MaximumBoundaryDistance":1,"MinimumCoverage":1,"MaximumGap":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"settings":` + string(rawSettings) + `,` + tc.fields + `}`
			_, err := decodeSchedulerConfiguration(raw, 1<<20)
			if (err == nil) != tc.valid {
				t.Fatalf("configuration valid=%t error=%v", tc.valid, err)
			}
		})
	}
	if _, err = decodeSchedulerConfiguration(`{"settings":`+string(rawSettings)+`} {}`, 1<<20); err == nil {
		t.Fatal("trailing configuration accepted")
	}
	if _, err = decodeSchedulerConfiguration(`{"settings":`+string(rawSettings)+`}`, 1); err == nil {
		t.Fatal("over-limit configuration accepted")
	}
}

func TestStaticRoutingAcknowledgementDoesNotReapplyOrRunWithControlsOn(t *testing.T) {
	for _, test := range []struct {
		name          string
		enabled       bool
		declared      bool
		appliedRV     string
		wantAppliedRV string
	}{
		{name: "completed revision is not reapplied", declared: true, appliedRV: "42", wantAppliedRV: "42"},
		{name: "unapplied declaration stays unapplied while ON", declared: true, enabled: true},
		{name: "declaration removal clears acknowledgement", appliedRV: "41"},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := &schedulerOwner{resourceVersion: "42", staticAppliedRV: test.appliedRV}
			owner.configuration.Settings.Controls.Enabled = test.enabled
			if test.declared {
				owner.configuration.StaticRouting = map[string]map[string]int{"current": {"physical": 1}}
			}
			// No controller is installed: these cases must not attempt application.
			owner.applyStaticRouting(context.Background())
			if owner.staticAppliedRV != test.wantAppliedRV {
				t.Fatalf("static application acknowledgement = %q, want %q", owner.staticAppliedRV, test.wantAppliedRV)
			}
		})
	}
}
