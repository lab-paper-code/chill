package main

import (
 "slices"
 "github.com/lab-paper-code/chill/internal/operatorconsole"
 "k8s.io/utils/ptr"
)

func coverageSummarize(out *operatorconsole.MeasurementCoverageResponse,base *measurementCoverageBase){
 summary:=operatorconsole.MeasurementCoverageSummary{CountsByDisplayStatus:map[string]*int{},RequestCount:ptr.To(len(out.Entities.Requests)),RunCount:ptr.To(len(out.Entities.Runs)),ProfileCount:ptr.To(len(out.Entities.Profiles)),SupportedCellCount:ptr.To(0),CompleteCellCount:ptr.To(0),CriteriaMetCellCount:ptr.To(0),ReusedCellCount:ptr.To(0),UnknownSupportCount:ptr.To(0),UnsupportedCellCount:ptr.To(0),FailedCellCount:ptr.To(0),CurrentFailedRequestCount:ptr.To(0),HistoricalFailedRequestCount:ptr.To(0)}
 for _,status:=range []string{"usable","insufficient","problem","running","unmeasured","unsupported","unknown"}{summary.CountsByDisplayStatus[status]=ptr.To(0)}
 cells:=map[string]operatorconsole.MeasurementCoverageCell{}
 actions:=map[string]int{}
 for _,cell:=range out.Cells{
  cells[cell.ID]=cell
  *summary.CountsByDisplayStatus[cell.Assessment.DisplayStatus]++
  switch cell.Support.Verdict{case "Compatible":*summary.SupportedCellCount++;case "Incompatible":*summary.UnsupportedCellCount++;default:*summary.UnknownSupportCount++}
  if cell.Assessment.DisplayStatus=="usable"{*summary.CompleteCellCount++}
  if cell.Assessment.Status=="criteria_met"{*summary.CriteriaMetCellCount++}
  if cell.Assessment.Scope.Reused&&cell.Assessment.DisplayStatus=="usable"{*summary.ReusedCellCount++}
  if cell.Assessment.DisplayStatus=="problem"{*summary.FailedCellCount++}
  for _,failure:=range cell.FailureSummary{if cell.Assessment.DisplayStatus=="usable"||actions["failure"]>=3{continue};out.NextActions=append(out.NextActions,operatorconsole.MeasurementNextAction{Kind:"failure",CellID:cell.ID,EntityID:failure.RequestID,ReasonCode:failure.Code,Message:failure.Message});actions["failure"]++}
  for _,variant:=range cell.Variants{if len(variant.BatchProgress.Missing)>0&&actions["missing-batches"]<3{out.NextActions=append(out.NextActions,operatorconsole.MeasurementNextAction{Kind:"missing-batches",CellID:cell.ID,VariantID:ptr.To(variant.ID),ReasonCode:"TargetBatchMissing",Message:"목표 batch의 부족 범위를 확인하세요"});actions["missing-batches"]++}}
  if cell.Assessment.Status=="unknown"&&cell.Support.Verdict!="Incompatible"&&actions["evidence"]<3{out.NextActions=append(out.NextActions,operatorconsole.MeasurementNextAction{Kind:"evidence",CellID:cell.ID,ReasonCode:"EvidenceUnconfirmed",Message:"지원 입력과 동결 근거를 확인하세요"});actions["evidence"]++}
 }
 for _,request:=range out.Entities.Requests{if request.Assessment.Status!="failed"{continue};resolved:=false;for _,id:=range request.CellIDs{resolved=resolved||cells[id].Assessment.DisplayStatus=="usable"};if resolved{*summary.HistoricalFailedRequestCount++}else{*summary.CurrentFailedRequestCount++}}
 for i:=range out.Groups{group:=&out.Groups[i];for _,id:=range group.CellIDs{cell:=cells[id];if cell.Assessment.DisplayStatus=="usable"{group.Complete++};switch cell.Support.Verdict{case "Compatible":*group.Supported++;case "Unknown":group.UnknownSupport++}}}
 if base.failures["compatibility"]!=nil||base.failures["inventory"]!=nil{summary.SupportedCellCount=nil;summary.UnsupportedCellCount=nil;for i:=range out.Groups{out.Groups[i].Supported=nil}}
 if base.failures["BEO"]!=nil||base.failures["Run"]!=nil{summary.CountsByDisplayStatus["unmeasured"]=nil}
 // Run IDs may be referenced by several views, but counts are identities.
 ids:=make([]string,0,len(out.Entities.Runs));for _,run:=range out.Entities.Runs{ids=append(ids,run.UID)};slices.Sort(ids);summary.RunCount=ptr.To(len(slices.Compact(ids)))
 out.Summary=summary
}
