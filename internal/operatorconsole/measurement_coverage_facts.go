package operatorconsole

import (
 "math"
 profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func AssessMeasurementBatch(facts profilederivation.MeasurementBatchFacts, minimumTrials int, reused, used bool) MeasurementAssessment {
 minimumTrials=max(3,minimumTrials)
 check:=func(code string,pass bool,message string) MeasurementCheck {result:="unknown";if pass {result="pass"};return MeasurementCheck{Code:code,Result:result,Message:message,SourceIDs:[]string{}}}
 knownCheck:=func(code string,pass bool,message string)MeasurementCheck{out:=check(code,pass,message);if !pass{out.Result="fail"};return out}
 finite:=func(v float64)bool{return !math.IsNaN(v)&&!math.IsInf(v,0)}
 ci:=facts.Confidence
 confidence:=ci!=nil&&finite(ci.Level)&&ci.Level>0&&ci.Level<1&&finite(ci.HalfWidth)&&ci.HalfWidth>=0
 checks:=[]MeasurementCheck{
  check("ExactConditionLineage",len(facts.Sources)>0,"동결 조건과 원시 근거 연결"),
  check("TargetBatch",facts.Batch>0,"해당 batch 실측"),
  knownCheck("ActiveTrials",facts.ActiveTrialCount>=minimumTrials,"독립 활성 반복 수"),
  knownCheck("IdleTrials",facts.IdleTrialCount>=minimumTrials,"공유 유휴 반복 수"),
  check("IncrementalConfidence",confidence,"기존 증분 에너지 신뢰구간"),
  check("ClientLatency",facts.P95Milliseconds!=nil&&finite(*facts.P95Milliseconds)&&facts.LatencySampleCount>1,"통합 client p95와 표본 수"),
  check("PowerSource",facts.PowerSource.Kind!=""&&facts.PowerSource.NodeName!="","실제 전력 출처"),
 }
 quality:=len(facts.Quality)>0
 for _,trial:=range facts.Quality {quality=quality&&trial.PolicyPassed!=nil&&*trial.PolicyPassed}
 qualityCheck:=check("FrozenPowerWindowPolicy",quality,"모든 활성·유휴 창의 동결 표본·gap·실패 정책")
 for _,trial:=range facts.Quality{if trial.PolicyPassed!=nil&&!*trial.PolicyPassed{qualityCheck.Result="fail"}}
 checks=append(checks,qualityCheck)
 for i:=range checks{for _,source:=range facts.Sources{checks[i].SourceIDs=append(checks[i].SourceIDs,source.Store+"/"+source.Key)}}
 return AssessMeasurement(MeasurementAssessmentInput{Accepted:true,Available:true,Reused:reused,SchedulerUsed:&used,Checks:checks})
}
