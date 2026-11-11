package application

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

func TestRecordActionStoresProfileReferences(t *testing.T) {
	ctx := context.Background()
	directory := cleanObservationDirectory(t)
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: directory, Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	profile := measuredAdoptionProfile()
	// Derived distributions are not part of the stored profile contract.
	for i := range profile.Batches {
		profile.Batches[i].ServiceDistribution = nil
	}
	for range 10 {
		profile.Batches[0].ServiceSamples = append(profile.Batches[0].ServiceSamples, profile.Batches[0].ServiceSamples...)
	}
	instance := serving.SchedulerInstance{ID: "instance", Profile: &profile}
	stage := serving.SchedulerCandidate{ID: "stage", Instances: []serving.SchedulerInstance{instance}}
	target := serving.SchedulerCandidate{
		ID: "target", Instances: []serving.SchedulerInstance{instance},
		Actions: []serving.Action{{ID: "route-stage", RouteTarget: &stage}},
	}
	at := time.Now().UTC()
	result := servingstate.OperationResult{
		Operation: servingstate.Operation{
			ID: "operation", Kind: servingstate.OperationRoute, Target: &target,
			Prepare: &servingstate.PrepareSpec{Instance: instance},
		},
		StartedAt: at, EndedAt: at.Add(time.Second), Status: servingstate.NativeOperationComplete,
	}
	full, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	service := &SchedulerService{observationLog: log}
	if err := service.RecordAction(result); err != nil {
		t.Fatal(err)
	}
	page, _, _, _, err := log.SchedulerObservationPage(ctx, at, time.Now().UTC().Add(time.Second), 1, 0, observation.SchedulerObservationSelection{})
	if err != nil || len(page) != 1 || page[0].Action == nil {
		t.Fatalf("action read failed: %v %v", page, err)
	}
	var expected servingstate.Operation
	var actual servingstate.OperationResult
	if err := json.Unmarshal(page[0].Action.Expected, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(page[0].Action.Actual, &actual); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []servingstate.Operation{expected, actual.Operation} {
		for _, stored := range []serving.SchedulerInstance{
			operation.Target.Instances[0], operation.Prepare.Instance,
			operation.Target.Actions[0].RouteTarget.Instances[0],
		} {
			if stored.Profile != nil || stored.ProfileID != profile.ID {
				t.Fatalf("action instance did not retain profile reference: %+v", stored)
			}
		}
	}
	if target.Instances[0].Profile != &profile || stage.Instances[0].Profile != &profile || result.Operation.Prepare.Instance.Profile != &profile {
		t.Fatal("storage projection mutated execution operation")
	}
	names, err := filepath.Glob(filepath.Join(directory, "*", "*", "scheduler.*.jsonl"))
	if err != nil || len(names) != 1 {
		t.Fatalf("scheduler files: %v %v", names, err)
	}
	stored, err := os.ReadFile(names[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("serviceSamples")) || len(stored) >= len(full)/10 || len(stored) > 100<<10 {
		t.Fatalf("action projection too large: full=%d stored=%d", len(full), len(stored))
	}
	t.Logf("action bytes: full result=%d stored expected+actual=%d", len(full), len(stored))
	catalogs, err := filepath.Glob(filepath.Join(directory, "*", "*", "catalog.*.jsonl"))
	if err != nil || len(catalogs) != 1 {
		t.Fatalf("catalog files: %v %v", catalogs, err)
	}
	catalog, err := os.Open(catalogs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	decoder := json.NewDecoder(catalog)
	for {
		var row observation.CatalogRow
		if err := decoder.Decode(&row); err == io.EOF {
			t.Fatal("action profile absent from catalog")
		} else if err != nil {
			t.Fatal(err)
		}
		if row.Kind == "profile" && row.Key == profile.ID {
			var restored serving.Profile
			if err := json.Unmarshal(row.Body, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored, profile) {
				t.Fatal("catalog did not preserve full action profile")
			}
			break
		}
	}
}
