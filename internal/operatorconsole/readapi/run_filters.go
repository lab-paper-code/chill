package readapi

import (
	"encoding/json"

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
	if filters["status"] != "" && operatorconsole.AssessProfilingRun(run).DisplayStatus != filters["status"] {
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
