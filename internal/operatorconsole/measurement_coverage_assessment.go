package operatorconsole

// MeasurementAssessment is a read projection, never a stored execution verdict.
type MeasurementAssessment struct {
 Status string `json:"status"`
 DisplayStatus string `json:"displayStatus"`
 Label string `json:"label"`
 Reasons []MeasurementReason `json:"reasons"`
 Execution *MeasurementExecution `json:"execution"`
 Evidence MeasurementEvidence `json:"evidence"`
 Checklist MeasurementChecklist `json:"checklist"`
 Scope MeasurementScope `json:"scope"`
}
type MeasurementReason struct { Code string `json:"code"`; Message string `json:"message"`; SourceIDs []string `json:"sourceIds"` }
type MeasurementExecution struct { Phase string `json:"phase"`; Generation int64 `json:"generation"`; ObservedGeneration int64 `json:"observedGeneration"` }
type MeasurementEvidence struct { Available bool `json:"available"`; Accepted bool `json:"accepted"`; Reasons []string `json:"reasons"` }
type MeasurementCheck struct { Code string `json:"code"`; Result string `json:"result"`; Message string `json:"message"`; SourceIDs []string `json:"sourceIds"` }
type MeasurementChecklist struct { CriteriaMet *bool `json:"criteriaMet"`; Version string `json:"version"`; Checks []MeasurementCheck `json:"checks"` }
type MeasurementScope struct { SchedulerUsed *bool `json:"schedulerUsed"`; Reused bool `json:"reused"`; Facts []string `json:"facts"` }

// MeasurementAssessmentInput separates execution, accepted evidence and reporting
// checks. Missing reporting statistics never invalidate an accepted result.
type MeasurementAssessmentInput struct {
 Execution *MeasurementExecution
 ExecutionCurrent bool
 Accepted bool
 Available bool
 Reused bool
 SchedulerUsed *bool
 Support string
 SourcesComplete bool
 Attempted bool
 MissingBatches []int
 Problems []MeasurementReason
 Reasons []MeasurementReason
 Checks []MeasurementCheck
 Facts []string
 Children []MeasurementAssessment
}

func AssessMeasurement(in MeasurementAssessmentInput) MeasurementAssessment {
 out := MeasurementAssessment{Status:"unknown", Reasons:append([]MeasurementReason{},in.Reasons...), Execution:in.Execution,
  Evidence:MeasurementEvidence{Available:in.Available,Accepted:in.Accepted,Reasons:[]string{}},
  Checklist:MeasurementChecklist{Version:"result-checklist.v1",Checks:append([]MeasurementCheck{},in.Checks...)},
  Scope:MeasurementScope{SchedulerUsed:in.SchedulerUsed,Reused:in.Reused,Facts:append([]string{},in.Facts...)}}
 if len(in.Children)>0 {
  complete,criteria,partial,problem,running,unknown:=true,true,false,false,false,false
  for _,child:=range in.Children { out.Reasons=append(out.Reasons,child.Reasons...);out.Checklist.Checks=append(out.Checklist.Checks,child.Checklist.Checks...);out.Evidence.Available=out.Evidence.Available||child.Evidence.Available;out.Evidence.Accepted=out.Evidence.Accepted||child.Evidence.Accepted;out.Scope.Reused=out.Scope.Reused||child.Scope.Reused; complete=complete&&(child.Status=="complete"||child.Status=="criteria_met"); criteria=criteria&&child.Status=="criteria_met"; partial=partial||child.Status=="partial"; problem=problem||child.DisplayStatus=="problem"; running=running||child.DisplayStatus=="running"; unknown=unknown||child.Status=="unknown" }
  for _,child:=range in.Children{out.Scope.Facts=append(out.Scope.Facts,child.Scope.Facts...);if child.Scope.SchedulerUsed!=nil{used:=*child.Scope.SchedulerUsed;if out.Scope.SchedulerUsed!=nil{used=used||*out.Scope.SchedulerUsed};out.Scope.SchedulerUsed=&used}}
  switch {case problem:out.Status="warning";case partial:out.Status="partial";case criteria:out.Status="criteria_met";case complete:out.Status="complete";case unknown:out.Status="unknown";case running:out.Status="running";default:out.Status="unmeasured"}
  if complete {out.Checklist.CriteriaMet=&criteria}
 } else {
  switch {
  case len(in.Problems)>0: out.Status="warning";out.Reasons=append(out.Reasons,in.Problems...)
  case in.Accepted:
   out.Status="complete"
   if len(in.MissingBatches)>0 {out.Status="partial"} else if !in.Reused && len(in.Checks)>0 {
    met:=true; for _,check:=range in.Checks {if check.Result!="pass" {met=false}}
    out.Checklist.CriteriaMet=&met; if met {out.Status="criteria_met"}
   }
  case in.Execution!=nil && (in.ExecutionCurrent||in.Execution.Generation==in.Execution.ObservedGeneration) && in.Execution.Phase=="Failed":out.Status="failed"
  case in.Execution!=nil && (in.ExecutionCurrent||in.Execution.Generation==in.Execution.ObservedGeneration) && (in.Execution.Phase=="Pending"||in.Execution.Phase=="Granted"||in.Execution.Phase=="Running"||in.Execution.Phase=="Measuring"||in.Execution.Phase=="Waiting"):out.Status="running"
  case in.Support=="Incompatible":out.Status="unsupported"
  case in.Support=="Compatible" && in.SourcesComplete && !in.Attempted:out.Status="unmeasured"
  }
 }
 if len(in.Children)>0&&len(in.Problems)>0{out.Status="warning";out.Reasons=append(out.Reasons,in.Problems...)}
 if in.Reused {v:=false;out.Checklist.CriteriaMet=&v}
 switch out.Status {case "criteria_met","complete":out.DisplayStatus="usable";out.Label="사용 가능";case "partial":out.DisplayStatus="insufficient";out.Label="부족";case "warning","failed":out.DisplayStatus="problem";out.Label="문제";case "running":out.DisplayStatus="running";out.Label="진행";case "unmeasured":out.DisplayStatus="unmeasured";out.Label="미측정";case "unsupported":out.DisplayStatus="unsupported";out.Label="지원 안 함";default:out.DisplayStatus="unknown";out.Label="확인 불가"}
 return out
}
