package main

import (
 "fmt"
 "slices"
 "sort"
 "strings"
 "time"

 api "github.com/lab-paper-code/chill/api/v1alpha1"
 "github.com/lab-paper-code/chill/internal/operatorconsole"
 profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
 "github.com/lab-paper-code/chill/internal/serving"
 "github.com/lab-paper-code/chill/internal/serving/httpapi"
 "k8s.io/utils/ptr"
)

func coverageCellID(model,class,path,mode string)string{return strings.Join([]string{model,class,path,mode},"/")}
func coverageRequestID(task api.BatchEnergyOptimization)string{return task.Namespace+"/"+task.Name+"/"+string(task.UID)}
func coverageBatches(values []int)[]int{out:=append([]int{},values...);sort.Ints(out);return slices.Compact(out)}
func coverageContains(values []int,batch int)bool{return slices.Contains(values,batch)}
func coverageReason(code,message string)operatorconsole.MeasurementReason{return operatorconsole.MeasurementReason{Code:code,Message:message,SourceIDs:[]string{}}}

func buildMeasurementCoverage(base *measurementCoverageBase,namespace string,query httpapi.MeasurementCoverageQuery)operatorconsole.MeasurementCoverageResponse{
 basis,supportBasis:="requested","representative-node-static"
 if len(query.Batches)>0{basis="explicit"};if query.Node!=""{supportBasis="selected-node-static"}
 out:=operatorconsole.MeasurementCoverageResponse{SchemaVersion:"measurement-coverage.v1",AssessmentVersion:"result-checklist.v1",SnapshotID:namespace+"/"+base.catalog.Revision+"/"+base.requests.ResourceVersion+"/"+base.runs.ResourceVersion,DataAsOf:base.oldest().UTC().Format(time.RFC3339Nano),Freshness:"fresh",Completeness:"complete",
  Scope:operatorconsole.MeasurementCoverageScope{Namespace:namespace,SupportBasis:supportBasis,GoalBasis:basis,TargetBatches:append([]int{},query.Batches...)},Sources:[]operatorconsole.MeasurementCoverageSource{},Groups:[]operatorconsole.MeasurementCoverageGroup{},Cells:[]operatorconsole.MeasurementCoverageCell{},NextActions:[]operatorconsole.MeasurementNextAction{},
  Entities:operatorconsole.MeasurementCoverageEntities{Requests:[]operatorconsole.MeasurementEntity{},Profiles:[]operatorconsole.MeasurementEntity{},Runs:[]operatorconsole.MeasurementRunEntity{}}}
 if time.Since(base.oldest())>30*time.Second||len(base.failures)>0{out.Freshness="stale"}
 for _,kind:=range []string{"inventory","compatibility","catalog","BEO","Run"}{
  source:=operatorconsole.MeasurementCoverageSource{Kind:kind,ResourceVersions:[]string{},Complete:true}
  switch kind{case "inventory":source.ResourceVersions=[]string{base.inventory.ModelSpecsResourceVersion,base.inventory.DeviceClassesResourceVersion,base.inventory.NodesResourceVersion};case "catalog":source.ResourceVersions=[]string{base.catalog.Revision};case "BEO":source.ResourceVersions=[]string{base.requests.ResourceVersion};case "Run":source.ResourceVersions=[]string{base.runs.ResourceVersion};case "compatibility":for _,row:=range base.compatibility.NodeRows{if row.Inputs.RuntimeDeclaration!=nil{source.ResourceVersions=append(source.ResourceVersions,row.Inputs.RuntimeDeclaration.ResourceVersion)}};slices.Sort(source.ResourceVersions);source.ResourceVersions=slices.Compact(source.ResourceVersions)}
  if err:=base.failures[kind];err!=nil{source.Complete=false;source.Error=&operatorconsole.MeasurementCoverageSourceError{Code:"SourceUnavailable",Message:"입력 갱신 실패: "+kind,Retryable:true};out.Completeness="partial"}
  out.Sources=append(out.Sources,source)
 }
 support:=map[string]operatorconsole.ModelCompatibilityView{}
 rows:=base.compatibility.Rows;if query.Node!=""{rows=base.compatibility.NodeRows}
 for _,row:=range rows{if query.Node==""||row.Selection.Node==query.Node{support[strings.Join([]string{row.Selection.ModelSpec,row.Selection.DeviceClass,row.Selection.ExecutionPath},"/")]=row}}
 classes:=append([]operatorconsole.InventoryClassView{},base.inventory.Classes...);slices.SortFunc(classes,func(a,b operatorconsole.InventoryClassView)int{return strings.Compare(a.Name,b.Name)})
 models:=append([]operatorconsole.InventoryModelView{},base.inventory.Models...);slices.SortFunc(models,func(a,b operatorconsole.InventoryModelView)int{return strings.Compare(a.Name,b.Name)})
 indices:=map[string]int{}
 for _,model:=range models{for _,class:=range classes{if query.Node!=""&&!slices.ContainsFunc(class.Nodes,func(node operatorconsole.InventoryNodeView)bool{return node.Name==query.Node}){continue};group:=operatorconsole.MeasurementCoverageGroup{Model:operatorconsole.MeasurementIdentity{Name:model.Name,UID:model.UID},DeviceClass:operatorconsole.MeasurementIdentity{Name:class.Name,UID:class.UID},CellIDs:[]string{},Supported:ptr.To(0)}
  for _,path:=range model.Spec.ExecutionPaths{for _,mode:=range class.PowerModes{
   id:=coverageCellID(model.Name,class.Name,path.Name,mode.Name)
   cell:=operatorconsole.MeasurementCoverageCell{ID:id,Model:group.Model,DeviceClass:group.DeviceClass,ExecutionPath:path.Name,PowerMode:mode.Name,Variants:[]operatorconsole.MeasurementCoverageVariant{},FailureSummary:[]operatorconsole.MeasurementFailure{},History:[]operatorconsole.MeasurementHistory{},Support:operatorconsole.MeasurementSupport{Verdict:"Unknown",Reasons:[]operatorconsole.ModelCompatibilityReason{}}}
   if row,found:=support[strings.Join([]string{model.Name,class.Name,path.Name},"/")];found{cell.Support.Verdict=row.Verdict;cell.Support.Reasons=row.Reasons;if row.Inputs.Node.Name!=""{cell.Support.Node=&operatorconsole.MeasurementIdentity{Name:row.Inputs.Node.Name,UID:row.Inputs.Node.UID}}}else{cell.Support.Reasons=append(cell.Support.Reasons,operatorconsole.ModelCompatibilityReason{Kind:"Unknown",Code:"SupportInputMissing",Message:"선택 Node의 지원 입력을 확인할 수 없습니다"})}
   indices[id]=len(out.Cells);out.Cells=append(out.Cells,cell);group.CellIDs=append(group.CellIDs,id)
  }};out.Groups=append(out.Groups,group)
 }}
 // Owner UID is the only run-to-request join; labels are not ownership proof.
 owners:=map[string][]api.ProfilingRun{}
 nativeRequests:=map[string]api.BatchEnergyOptimization{};for _,task:=range base.requests.Items{nativeRequests[string(task.UID)]=task}
 for _,run:=range base.runs.Items{for _,owner:=range run.OwnerReferences{task,exists:=nativeRequests[string(owner.UID)];if exists&&run.Namespace==task.Namespace&&owner.Name==task.Name&&owner.Controller!=nil&&*owner.Controller&&owner.APIVersion==api.GroupVersion.String()&&owner.Kind=="BatchEnergyOptimization"{owners[string(owner.UID)]=append(owners[string(owner.UID)],run);break}}}
 profileByRequest:=map[string][]serving.Profile{}
 for _,profile:=range base.catalog.Profiles{if facts,ok:=base.catalog.MeasurementFacts[profile.ID];ok{profileByRequest[facts.Request.Namespace+"/"+facts.Request.Name+"/"+facts.Request.UID]=append(profileByRequest[facts.Request.Namespace+"/"+facts.Request.Name+"/"+facts.Request.UID],profile)}}
 adopted:=map[string]bool{}
 for _,profile:=range base.catalog.Profiles{adopted[profile.ID]=base.adopted==nil||base.adopted[profile.ID]}
 // Facts from normally superseded profiles remain valid measurements, but
 // are not presented as currently selected scheduler inputs.
 factIDs:=make([]string,0,len(base.catalog.MeasurementFacts));for id:=range base.catalog.MeasurementFacts{factIDs=append(factIDs,id)};slices.Sort(factIDs)
 for _,id:=range factIDs{facts:=base.catalog.MeasurementFacts[id];requestID:=facts.Request.Namespace+"/"+facts.Request.Name+"/"+facts.Request.UID;if facts.Request.Namespace!=namespace||len(profileByRequest[requestID])>0{continue};profileByRequest[requestID]=[]serving.Profile{{ID:id,Condition:facts.Condition,NodeName:facts.Condition.MetricSource.NodeName}}}
 for _,task:=range base.requests.Items{
  if query.Node!=""&&task.Spec.State.TargetNode!=query.Node{continue}
  candidate:=task.Spec.State.Candidate;id:=coverageCellID(candidate.ModelSpec,candidate.DeviceClass,candidate.ExecutionPath,candidate.PowerMode)
  index,exists:=indices[id];requestID:=coverageRequestID(task)
  assessment:=assessMeasurementRequest(task,base.catalog)
  if assessment.Evidence.Accepted{used:=false;for _,profile:=range profileByRequest[requestID]{used=used||adopted[profile.ID]};assessment.Scope.SchedulerUsed=&used;if !used{assessment.Scope.Facts=append(assessment.Scope.Facts,"현재 스케줄러 미사용")}}
  cellIDs:=[]string{};if exists{cellIDs=append(cellIDs,id)}
  out.Entities.Requests=append(out.Entities.Requests,operatorconsole.MeasurementEntity{UID:string(task.UID),ID:requestID,Assessment:assessment,CellIDs:cellIDs})
  if !exists{continue};cell:=&out.Cells[index]
  cell.History=append(cell.History,operatorconsole.MeasurementHistory{RequestID:requestID,Status:assessment.Status})
  profileList:=profileByRequest[requestID]
  if len(profileList)==0{
   variantID:=operatorconsole.MeasurementVariantID(task.Spec)
   variant:=coverageFindVariant(cell,variantID,task.Spec,basis)
   variant.RequestIDs=append(variant.RequestIDs,requestID)
   if variant.Assessment.Status==""||assessment.DisplayStatus=="problem"||assessment.Status=="running"{variant.Assessment=assessment}
   variant.Assessment.Reasons=append(variant.Assessment.Reasons,coverageReason("PlannedConditionUnconfirmed","요청 spec만 확인했으며 동결 실행 조건의 중복 여부는 검사에서 확인해야 합니다"))
   targets:=[]int{};for _,batch:=range task.Spec.BatchSizes{targets=append(targets,int(batch))}
   variant.BatchProgress.Target=coverageBatches(append(variant.BatchProgress.Target,targets...))
   // A request spec is planned-only. It does not prove a frozen measured variant.
   for _,run:=range owners[string(task.UID)]{variant.RunIDs=append(variant.RunIDs,string(run.UID))}
  }
  for _,profile:=range profileList{
   facts:=base.catalog.MeasurementFacts[profile.ID]
   variantID:=operatorconsole.MeasurementVariantID(facts.Condition)
   variant:=coverageFindVariant(cell,variantID,facts.Condition,basis)
   variant.RequestIDs=append(variant.RequestIDs,requestID);variant.ProfileIDs=append(variant.ProfileIDs,profile.ID)
   variant.BatchProgress.Target=coverageBatches(append(variant.BatchProgress.Target,facts.Condition.PlannedBatchSizes...))
   for _,run:=range owners[string(task.UID)]{variant.RunIDs=append(variant.RunIDs,string(run.UID))}
   for _,batch:=range facts.Batches{
    row:=coverageResultRow(profile,facts,batch,"direct")
    row.Assessment.Scope.SchedulerUsed=ptr.To(adopted[profile.ID])
    if !adopted[profile.ID]{row.Assessment.Scope.Facts=append(row.Assessment.Scope.Facts,"현재 스케줄러 미사용")}
    if profile.Reuse!=nil{row.RowKind="confirmation";row.SourceProfileID=ptr.To(profile.Reuse.SourceProfileID);row.Reuse=profile.Reuse}
    variant.ResultRows=append(variant.ResultRows,row)
    variant.BatchProgress.Measured=append(variant.BatchProgress.Measured,batch.Batch)
    if row.Assessment.Status=="criteria_met"{variant.BatchProgress.CriteriaMet=append(variant.BatchProgress.CriteriaMet,batch.Batch)}
   }
   if profile.Reuse!=nil{if source,ok:=base.catalog.MeasurementFacts[profile.Reuse.SourceProfileID];ok&&source.Request.Namespace==namespace{for _,batch:=range source.Batches{row:=coverageResultRow(profile,source,batch,"source-reference");row.ProfileID=profile.Reuse.SourceProfileID;row.SourceProfileID=ptr.To(profile.Reuse.SourceProfileID);row.TotalJoulesPerItem=nil;row.Reuse=profile.Reuse;variant.ResultRows=append(variant.ResultRows,row)}}}
   used:=adopted[profile.ID]
   profileAssessment:=operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{Accepted:true,Available:true,Reused:profile.Reuse!=nil,SchedulerUsed:&used})
   out.Entities.Profiles=append(out.Entities.Profiles,operatorconsole.MeasurementEntity{UID:facts.Request.UID,ID:profile.ID,Assessment:profileAssessment,CellIDs:[]string{id}})
  }
  if assessment.DisplayStatus=="problem"{cell.FailureSummary=append(cell.FailureSummary,operatorconsole.MeasurementFailure{RequestID:requestID,Code:task.Status.Outcome,Message:coverageFailureMessage(task),Relation:"unresolved"})}
  for _,run:=range owners[string(task.UID)]{runAssessment:=operatorconsole.AssessProfilingRun(run);if runAssessment.DisplayStatus=="problem"{failure:=operatorconsole.MeasurementFailure{RequestID:requestID,RunID:string(run.UID),Batch:ptr.To(int(run.Spec.Measurement.Batch)),Relation:"unresolved"};for _,reason:=range runAssessment.Reasons{failure.Code=reason.Code;failure.Message=coverageShortMessage(reason.Message)};if run.Status.RuntimeTermination!=nil{failure.ExitCode=ptr.To(int(run.Status.RuntimeTermination.ExitCode))};cell.FailureSummary=append(cell.FailureSummary,failure)}}
 }
 for _,run:=range base.runs.Items{if query.Node!=""&&run.Spec.TargetNode!=query.Node{continue};candidate:=run.Spec.Candidate;id:=coverageCellID(candidate.ModelSpec,candidate.DeviceClass,candidate.ExecutionPath,candidate.PowerMode);cellIDs:=[]string{};if _,exists:=indices[id];exists{cellIDs=append(cellIDs,id)};assessment:=operatorconsole.AssessProfilingRun(run);out.Entities.Runs=append(out.Entities.Runs,operatorconsole.MeasurementRunEntity{UID:string(run.UID),Status:assessment.DisplayStatus,Label:assessment.Label,CellIDs:cellIDs})}
 for i:=range out.Cells{
  cell:=&out.Cells[i];children:=[]operatorconsole.MeasurementAssessment{}
  for j:=range cell.Variants{variant:=&cell.Variants[j];progress:=&variant.BatchProgress
   if len(query.Batches)>0{progress.Target=append([]int{},query.Batches...)}
   progress.Target=coverageBatches(progress.Target);progress.Measured=coverageBatches(progress.Measured);progress.CriteriaMet=coverageBatches(progress.CriteriaMet);progress.InProgress=coverageBatches(progress.InProgress)
   for _,batch:=range progress.Target{if !coverageContains(progress.Measured,batch){if len(variant.ProfileIDs)==0{progress.Unknown=append(progress.Unknown,batch)}else{progress.Missing=append(progress.Missing,batch)}}}
   if len(variant.ProfileIDs)>0{checks:=[]operatorconsole.MeasurementAssessment{};for _,row:=range variant.ResultRows{if row.RowKind!="source-reference"&&coverageContains(progress.Target,row.Batch){checks=append(checks,row.Assessment)}}
    used,reused:=false,false;for _,id:=range variant.ProfileIDs{used=used||adopted[id]};for _,row:=range variant.ResultRows{reused=reused||row.RowKind=="confirmation"}
    variant.Assessment=operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{Accepted:true,Available:true,Reused:reused,SchedulerUsed:&used,MissingBatches:progress.Missing})
    if len(progress.Missing)==0&&len(checks)>0{variant.Assessment=operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{Children:checks,Accepted:true,Available:true,Reused:reused,SchedulerUsed:&used})}
   }
   children=append(children,variant.Assessment)
  }
  cell.VariantCount=len(cell.Variants)
  if len(cell.Variants)==1{cell.BatchProgress=&cell.Variants[0].BatchProgress;for _,row:=range cell.Variants[0].ResultRows{if row.RowKind!="source-reference"{cell.Representative=&operatorconsole.MeasurementRepresentative{VariantID:cell.Variants[0].ID,Batch:row.Batch,IncrementalJoulesPerItem:row.IncrementalJoulesPerItem,P95Milliseconds:row.P95Milliseconds};break}}}
  cell.Assessment=operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{Children:children,Support:cell.Support.Verdict,SourcesComplete:base.failures["BEO"]==nil&&base.failures["Run"]==nil,Attempted:len(cell.History)>0})
  if cell.Support.Verdict=="Incompatible"&&cell.Assessment.Evidence.Accepted{cell.Assessment=operatorconsole.AssessMeasurement(operatorconsole.MeasurementAssessmentInput{Accepted:true,Available:true,Problems:[]operatorconsole.MeasurementReason{coverageReason("CurrentDeclarationConflict","보존 측정과 현재 지원 선언이 일치하지 않습니다")}})}
 }
 coverageSummarize(&out,base)
 return out
}

func coverageFindVariant(cell *operatorconsole.MeasurementCoverageCell,id string,condition any,basis string)*operatorconsole.MeasurementCoverageVariant{
 for i:=range cell.Variants{if cell.Variants[i].ID==id{return &cell.Variants[i]}}
 cell.Variants=append(cell.Variants,operatorconsole.MeasurementCoverageVariant{ID:id,Condition:condition,ResultRows:[]operatorconsole.ResultRow{},RequestIDs:[]string{},RunIDs:[]string{},ProfileIDs:[]string{},BatchProgress:operatorconsole.MeasurementBatchProgress{Basis:basis,Target:[]int{},Measured:[]int{},CriteriaMet:[]int{},Missing:[]int{},InProgress:[]int{},Unknown:[]int{}}})
 return &cell.Variants[len(cell.Variants)-1]
}

func coverageResultRow(profile serving.Profile,facts profilederivation.MeasurementFacts,batch profilederivation.MeasurementBatchFacts,kind string)operatorconsole.ResultRow{
 row:=operatorconsole.MeasurementResultRow(facts,batch,profile.ID,profile.Reuse!=nil,true)
 row.RowKind=kind
 row.ID=fmt.Sprintf("%s/%s/%d",profile.ID,kind,batch.Batch)
 return row
}
func coverageFailureMessage(task api.BatchEnergyOptimization)string{
 message:=task.Status.Outcome
 for _,condition:=range task.Status.Conditions{if condition.Message!=""{message=condition.Message}}
 message=strings.Join(strings.Fields(message)," ");runes:=[]rune(message);if len(runes)>160{message=string(runes[:160])};return message
}
func coverageShortMessage(message string)string{message=strings.Join(strings.Fields(message)," ");runes:=[]rune(message);if len(runes)>160{return string(runes[:160])};return message}
