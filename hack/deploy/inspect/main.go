// Command inspect applies production current-measurement selection to a retained
// individually-assessed coverage snapshot. It never writes measurement objects.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type measurementTask struct {
	Kind              string `json:"kind"`
	metav1.ObjectMeta `json:"metadata"`
	Status            struct {
		Phase              string             `json:"phase"`
		ObservedGeneration int64              `json:"observedGeneration"`
		Conditions         []metav1.Condition `json:"conditions"`
	} `json:"status"`
}

type measurementList struct {
	Items []measurementTask `json:"items"`
}

type executionState struct {
	Generation, ObservedGeneration int64
	Phase                          string
	Complete                       *metav1.Condition
	Deleting                       bool
}

func executionStates(tasks measurementList) map[string]executionState {
	states := make(map[string]executionState, len(tasks.Items))
	for _, task := range tasks.Items {
		if task.Kind != "" && task.Kind != "BatchEnergyOptimization" {
			continue
		}
		state := executionState{Generation: task.Generation, ObservedGeneration: task.Status.ObservedGeneration, Phase: task.Status.Phase, Deleting: task.DeletionTimestamp != nil}
		for i := range task.Status.Conditions {
			if task.Status.Conditions[i].Type == "MeasurementComplete" {
				state.Complete = &task.Status.Conditions[i]
				break
			}
		}
		states[task.Namespace+"/"+task.Name+"/"+string(task.UID)] = state
	}
	return states
}

func load(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	err = json.NewDecoder(file).Decode(target)
	return errors.Join(err, file.Close())
}

func measurementTime(task measurementTask, profileTime time.Time) time.Time {
	if task.Status.Phase != string(api.MeasurementPhaseComplete) {
		profileTime = time.Time{}
	}
	result := profilederivation.MeasurementTime(profileTime, nil)
	if task.Status.ObservedGeneration != task.Generation || (task.Status.Phase != string(api.MeasurementPhaseComplete) && task.Status.Phase != string(api.MeasurementPhaseFailed)) {
		return result
	}
	for i, condition := range task.Status.Conditions {
		if condition.Type == "MeasurementComplete" && condition.ObservedGeneration == task.Generation {
			return profilederivation.MeasurementTime(profileTime, task.Status.Conditions[i:i+1])
		}
	}
	return result
}

func changedExecutions(tasks, assessedTasks measurementList) []string {
	actual, assessed := executionStates(tasks), executionStates(assessedTasks)
	changed := make([]string, 0)
	for id, state := range actual {
		if previous, ok := assessed[id]; !ok || !reflect.DeepEqual(state, previous) {
			changed = append(changed, id)
		}
	}
	for id := range assessed {
		if _, ok := actual[id]; !ok {
			changed = append(changed, id)
		}
	}
	return changed
}

func inspect(cellsPath, requestsPath, profilesPath, coveragePath, assessedRequestsPath string) (map[string]any, error) {
	var cells []queryapi.MeasurementCoverageCell
	var tasks, assessedTasks measurementList
	var profiles struct {
		Profiles []struct {
			ID         string    `json:"id"`
			MeasuredAt time.Time `json:"measuredAt"`
		} `json:"profiles"`
	}
	var before struct {
		DataAsOf string                               `json:"dataAsOf"`
		Sources  []queryapi.MeasurementCoverageSource `json:"sources"`
		Summary  struct {
			Counts map[string]*int `json:"countsByDisplayStatus"`
		} `json:"summary"`
		Cells []struct {
			ID         string `json:"id"`
			Assessment struct {
				DisplayStatus string `json:"displayStatus"`
			} `json:"assessment"`
		} `json:"cells"`
	}
	for _, input := range []struct {
		path string
		into any
	}{{cellsPath, &cells}, {requestsPath, &tasks}, {profilesPath, &profiles}, {coveragePath, &before}, {assessedRequestsPath, &assessedTasks}} {
		if err := load(input.path, input.into); err != nil {
			return nil, err
		}
	}
	beforeStatus := make(map[string]string, len(before.Cells))
	for _, cell := range before.Cells {
		beforeStatus[cell.ID] = cell.Assessment.DisplayStatus
	}
	sourceOK := map[string]bool{}
	for _, source := range before.Sources {
		sourceOK[source.Kind] = source.Complete && source.Error == nil
	}
	sourcesComplete := sourceOK["BEO"] && sourceOK["Run"]
	changed := changedExecutions(tasks, assessedTasks)
	if len(changed) > 0 {
		sort.Strings(changed)
		return map[string]any{
			"projectionAvailable": false, "reason": "native execution changed since the assessed variant snapshot; refresh assessed inputs",
			"changedNativeMeasurements": changed, "countsByDisplayStatus": nil, "selectedCells": nil,
			"currentFailedMeasurements": nil, "currentMeasurements": nil, "profileRecordsInput": len(profiles.Profiles),
			"countsBefore": before.Summary.Counts, "sourcesComplete": sourcesComplete, "coverageDataAsOf": before.DataAsOf,
		}, nil
	}
	represented := make(map[string]bool)
	times := make(map[string]time.Time, len(profiles.Profiles))
	for _, profile := range profiles.Profiles {
		id, _, _ := strings.Cut(profile.ID, "/reuse-")
		if profile.MeasuredAt.After(times[id]) {
			times[id] = profile.MeasuredAt
		}
	}
	names := make(map[string]string, len(tasks.Items))
	replacements := map[string]string{}
	for _, task := range tasks.Items {
		if task.Kind != "" && task.Kind != "BatchEnergyOptimization" {
			continue
		}
		id := task.Namespace + "/" + task.Name + "/" + string(task.UID)
		names[task.Namespace+"/"+task.Name] = id
		if name := task.Annotations[profilederivation.ReplacesMeasurementAnnotation]; name != "" && task.DeletionTimestamp == nil {
			replacements[id] = task.Namespace + "/" + name
		}
		times[id] = measurementTime(task, times[id])
	}
	replaced := profilederivation.ReplacedMeasurements(replacements, names)
	counts := map[string]int{}
	changes := []map[string]any{}
	current := []map[string]any{}
	failed := 0
	for _, cell := range cells {
		capacity := 0
		for _, variant := range cell.Variants {
			capacity += len(variant.RequestIDs)
		}
		candidates := make([]profilederivation.MeasurementSelection, 0, capacity)
		for _, variant := range cell.Variants {
			for _, id := range variant.RequestIDs {
				represented[id] = true
				candidates = append(candidates, profilederivation.MeasurementSelection{
					ID: id, Model: cell.Model.Name, DeviceClass: cell.DeviceClass.Name,
					ExecutionPath: cell.ExecutionPath, PowerMode: cell.PowerMode, MeasuredAt: times[id],
				})
			}
		}
		var selected *profilederivation.MeasurementSelection
		if chosen := profilederivation.CurrentMeasurements(candidates, replaced); len(chosen) > 0 {
			selected = &chosen[0]
		}
		after := queryapi.AssessMeasurementCoverageCell(cell, selected, sourcesComplete)
		counts[after.Assessment.DisplayStatus]++
		if selected != nil {
			current = append(current, map[string]any{"cell": cell.ID, "selection": selected, "status": after.Assessment.Status, "displayStatus": after.Assessment.DisplayStatus})
			if after.Assessment.Status == queryapi.MeasurementFailed {
				failed++
			}
		}
		if beforeStatus[cell.ID] != after.Assessment.DisplayStatus {
			changes = append(changes, map[string]any{"cell": cell.ID, "before": beforeStatus[cell.ID], "after": after.Assessment.DisplayStatus, "currentMeasurement": selected, "reasons": after.Assessment.Reasons})
		}
	}
	missing := []string{}
	for _, id := range names {
		if !represented[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	summaryCounts := make(map[string]any, len(counts))
	for status, count := range counts {
		summaryCounts[status] = count
	}
	if !sourcesComplete {
		summaryCounts["unmeasured"] = nil
	}
	return map[string]any{
		"basis":                           "individually assessed variant snapshot + native BEO annotations/timestamps; production CurrentMeasurements and AssessMeasurementCoverageCell; not hardware/performance equivalence or live post-activation evidence",
		"projectionAvailable":             true,
		"runtimeImageIsValidityCondition": false,
		"countsByDisplayStatus":           summaryCounts, "selectedCells": len(current), "currentFailedMeasurements": failed,
		"currentMeasurements": current, "changedCells": changes, "profileRecordsInput": len(profiles.Profiles),
		"countsBefore": before.Summary.Counts, "sourcesComplete": sourcesComplete, "coverageDataAsOf": before.DataAsOf,
		"nativeMeasurements": len(names), "requestsWithoutAssessedVariant": missing,
	}, nil
}

func main() {
	cells := flag.String("cells", "", "individually assessed coverage cell array")
	requests := flag.String("requests", "", "native BEO list with annotations and conditions")
	profiles := flag.String("profiles", "", "public profile snapshot with original measuredAt")
	coverage := flag.String("coverage", "", "public coverage snapshot for source completeness and before statuses")
	assessedRequests := flag.String("assessed-requests", "", "native BEO snapshot accompanying the assessed variant input")
	flag.Parse()
	report, err := inspect(*cells, *requests, *profiles, *coverage, *assessedRequests)
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
