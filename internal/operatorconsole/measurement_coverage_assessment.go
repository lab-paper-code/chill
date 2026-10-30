package operatorconsole

// MeasurementAssessment is a read projection, never a stored execution verdict.
type MeasurementAssessment struct {
	Status        string                `json:"status"`
	DisplayStatus string                `json:"displayStatus"`
	Label         string                `json:"label"`
	Reasons       []MeasurementReason   `json:"reasons"`
	Execution     *MeasurementExecution `json:"execution"`
	Evidence      MeasurementEvidence   `json:"evidence"`
	Checklist     MeasurementChecklist  `json:"checklist"`
	Scope         MeasurementScope      `json:"scope"`
}
type MeasurementReason struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	SourceIDs []string `json:"sourceIds"`
}
type MeasurementExecution struct {
	Phase              string `json:"phase"`
	Generation         int64  `json:"generation"`
	ObservedGeneration int64  `json:"observedGeneration"`
}
type MeasurementEvidence struct {
	Available bool     `json:"available"`
	Accepted  bool     `json:"accepted"`
	Reasons   []string `json:"reasons"`
}
type MeasurementCheck struct {
	Code      string   `json:"code"`
	Result    string   `json:"result"`
	Message   string   `json:"message"`
	SourceIDs []string `json:"sourceIds"`
}
type MeasurementChecklist struct {
	CriteriaMet *bool              `json:"criteriaMet"`
	Version     string             `json:"version"`
	Checks      []MeasurementCheck `json:"checks"`
}
type MeasurementScope struct {
	SchedulerUsed *bool    `json:"schedulerUsed"`
	Reused        bool     `json:"reused"`
	Facts         []string `json:"facts"`
}

// MeasurementAssessmentInput separates execution, accepted evidence and reporting
// checks. Missing reporting statistics never invalidate an accepted result.
type MeasurementAssessmentInput struct {
	Execution        *MeasurementExecution
	ExecutionCurrent bool
	Accepted         bool
	Available        bool
	Reused           bool
	SchedulerUsed    *bool
	Support          string
	SourcesComplete  bool
	Attempted        bool
	MissingBatches   []int
	Problems         []MeasurementReason
	Reasons          []MeasurementReason
	Checks           []MeasurementCheck
	Facts            []string
	Children         []MeasurementAssessment
}

func AssessMeasurement(in MeasurementAssessmentInput) MeasurementAssessment {
	out := MeasurementAssessment{
		Status: MeasurementUnknown, Reasons: append([]MeasurementReason{}, in.Reasons...), Execution: in.Execution,
		Evidence:  MeasurementEvidence{Available: in.Available, Accepted: in.Accepted, Reasons: []string{}},
		Checklist: MeasurementChecklist{Version: "result-checklist.v1", Checks: append([]MeasurementCheck{}, in.Checks...)},
		Scope: MeasurementScope{
			SchedulerUsed: in.SchedulerUsed, Reused: in.Reused, Facts: append([]string{}, in.Facts...),
		},
	}
	if len(in.Children) > 0 {
		assessMeasurementChildren(&out, in.Children)
	} else {
		assessSingleMeasurement(&out, in)
	}
	if len(in.Problems) > 0 {
		out.Status = MeasurementWarning
		out.Reasons = append(out.Reasons, in.Problems...)
	}
	if in.Reused {
		value := false
		out.Checklist.CriteriaMet = &value
	}
	out.DisplayStatus, out.Label = measurementDisplay(out.Status)
	return out
}

func assessMeasurementChildren(out *MeasurementAssessment, children []MeasurementAssessment) {
	counts := struct{ complete, criteria, partial, problem, running, unknown int }{}
	for _, child := range children {
		out.Reasons = append(out.Reasons, child.Reasons...)
		out.Checklist.Checks = append(out.Checklist.Checks, child.Checklist.Checks...)
		out.Evidence.Available = out.Evidence.Available || child.Evidence.Available
		out.Evidence.Accepted = out.Evidence.Accepted || child.Evidence.Accepted
		out.Scope.Reused = out.Scope.Reused || child.Scope.Reused
		out.Scope.Facts = append(out.Scope.Facts, child.Scope.Facts...)
		if child.Scope.SchedulerUsed != nil {
			used := *child.Scope.SchedulerUsed
			if out.Scope.SchedulerUsed != nil {
				used = used || *out.Scope.SchedulerUsed
			}
			out.Scope.SchedulerUsed = &used
		}
		switch child.Status {
		case MeasurementComplete:
			counts.complete++
		case MeasurementCriteriaMet:
			counts.criteria++
		case MeasurementPartial:
			counts.partial++
		case MeasurementWarning, MeasurementFailed:
			counts.problem++
		case MeasurementUnknown:
			counts.unknown++
		case MeasurementRunning:
			counts.running++
		}
	}
	complete := counts.complete+counts.criteria == len(children)
	criteria := counts.criteria == len(children)
	switch {
	case counts.problem > 0:
		out.Status = MeasurementWarning
	case counts.partial > 0:
		out.Status = MeasurementPartial
	case criteria:
		out.Status = MeasurementCriteriaMet
	case complete:
		out.Status = MeasurementComplete
	case counts.unknown > 0:
		out.Status = MeasurementUnknown
	case counts.running > 0:
		out.Status = MeasurementRunning
	default:
		out.Status = MeasurementUnmeasured
	}
	if complete {
		out.Checklist.CriteriaMet = &criteria
	}
}

func assessSingleMeasurement(out *MeasurementAssessment, in MeasurementAssessmentInput) {
	if len(in.Problems) > 0 {
		return
	}
	if in.Accepted {
		assessAcceptedMeasurement(out, in)
		return
	}
	if in.Execution != nil && (in.ExecutionCurrent || in.Execution.Generation == in.Execution.ObservedGeneration) {
		switch in.Execution.Phase {
		case "Failed":
			out.Status = MeasurementFailed
			return
		case "Pending", "Granted", "Running", "Measuring", "Waiting":
			out.Status = MeasurementRunning
			return
		}
	}
	switch {
	case in.Support == "Incompatible":
		out.Status = MeasurementUnsupported
	case in.Support == "Compatible" && in.SourcesComplete && !in.Attempted:
		out.Status = MeasurementUnmeasured
	}
}

func assessAcceptedMeasurement(out *MeasurementAssessment, in MeasurementAssessmentInput) {
	out.Status = MeasurementComplete
	if len(in.MissingBatches) > 0 {
		out.Status = MeasurementPartial
		return
	}
	if in.Reused || len(in.Checks) == 0 {
		return
	}
	met := true
	for _, check := range in.Checks {
		if check.Result != "pass" {
			met = false
		}
	}
	out.Checklist.CriteriaMet = &met
	if met {
		out.Status = MeasurementCriteriaMet
	}
}

func measurementDisplay(status string) (string, string) {
	switch status {
	case MeasurementCriteriaMet, MeasurementComplete:
		return MeasurementUsable, "사용 가능"
	case MeasurementPartial:
		return MeasurementInsufficient, "부족"
	case MeasurementWarning, MeasurementFailed:
		return MeasurementProblem, "문제"
	case MeasurementRunning:
		return MeasurementRunning, "진행"
	case MeasurementUnmeasured:
		return MeasurementUnmeasured, "미측정"
	case MeasurementUnsupported:
		return MeasurementUnsupported, "지원 안 함"
	default:
		return MeasurementUnknown, "확인 불가"
	}
}
