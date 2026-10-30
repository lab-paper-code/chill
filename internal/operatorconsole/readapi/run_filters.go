package readapi

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
)

func canonicalRunGroup(raw string) (string, bool) {
	var group map[string]any
	if json.Unmarshal([]byte(raw), &group) != nil || group == nil {
		return "", false
	}
	encoded, err := json.Marshal(group)
	return string(encoded), err == nil
}

func matchesRunFilters(run api.ProfilingRun, filters map[string]string) bool {
	candidate := run.Spec.Candidate
	cpu := ""
	if run.Spec.Measurement.CPUQuota != nil {
		cpu = run.Spec.Measurement.CPUQuota.String()
	}
	values := map[string]string{"model": candidate.ModelSpec, "deviceClass": candidate.DeviceClass,
		queryNode: run.Spec.TargetNode, "path": candidate.ExecutionPath, "powerMode": candidate.PowerMode, "cpu": cpu}
	for key, value := range values {
		if filters[key] != "" && filters[key] != value {
			return false
		}
	}
	if filters[queryRunStatus] != "" &&
		operatorconsole.AssessProfilingRun(run).DisplayStatus != filters[queryRunStatus] {
		return false
	}
	if uid := filters["parentUID"]; uid != "" {
		owned := false
		for _, owner := range run.OwnerReferences {
			owned = owned || owner.Controller != nil && *owner.Controller && owner.APIVersion == api.GroupVersion.String() &&
				owner.Kind == "BatchEnergyOptimization" && string(owner.UID) == uid
		}
		if !owned {
			return false
		}
	}
	if group := filters["group"]; group != "" {
		raw, _ := json.Marshal(run.Spec)
		var condition map[string]any
		if json.Unmarshal(raw, &condition) != nil {
			return false
		}
		condition["namespace"] = run.Namespace
		delete(condition["measurement"].(map[string]any), "batch")
		encoded, _ := json.Marshal(condition)
		if string(encoded) != group {
			return false
		}
	}
	return true
}

func sortRuns(runs []api.ProfilingRun, q listQuery) {
	if q.sort == "" {
		return
	}
	// Project status once, rather than rebuilding assessments in the comparator.
	statuses := map[string]string{}
	if q.sort == queryRunStatus {
		for _, run := range runs {
			statuses[run.Name] = operatorconsole.AssessProfilingRun(run).DisplayStatus
		}
	}
	slices.SortFunc(runs, func(a, b api.ProfilingRun) int {
		var result int
		switch q.sort {
		case "name":
			result = strings.Compare(a.Name, b.Name)
		case "created":
			result = a.CreationTimestamp.Time.Compare(b.CreationTimestamp.Time)
		case "model":
			result = strings.Compare(a.Spec.Candidate.ModelSpec, b.Spec.Candidate.ModelSpec)
		case "powerMode":
			result = strings.Compare(a.Spec.Candidate.PowerMode, b.Spec.Candidate.PowerMode)
		case queryRunStatus:
			result = strings.Compare(statuses[a.Name], statuses[b.Name])
		}
		if q.order == queryOrderDesc {
			result = -result
		}
		return cmp.Or(result, strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name), cmp.Compare(a.UID, b.UID))
	})
}
