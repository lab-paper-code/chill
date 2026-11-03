"use client"

import Link from "next/link"
import { ApiError, useChillQueries } from "@/lib/client"
import { Button } from "@/components/ui/button"
import { campaignScoreboard, comparisonValidity, decodeExperiment, definitionComparisonReason, definitionComparisons, definitionPolicy, experimentOutcome, experimentPath, historicalPublicationIDs, injectionRows, experimentRows, object, publishedText, recordedComparisons, settingsPatchEntries, settingsPatchKeyLabel, stageLoadParts, type Experiment, type ExperimentStage, type Published } from "@/lib/experiments"
import { formatMeasurementDuration, formatMeasurementNumber, formatMeasurementTime } from "@/lib/measurements"

// Only explicit historical comparison references may request another publication.
export function useHistoricalExperiments(publications:Experiment[],catalog:Experiment[]=publications,enabled=true) {
  const ids=[...new Set(publications.flatMap(historicalPublicationIDs).filter(id=>!catalog.some(run=>run.id===id)&&!publications.some(run=>run.id===id)))]
  const queries=useChillQueries<Experiment>(ids.map(experimentPath),{enabled,decoder:decodeExperiment})
  const fetched=queries.flatMap((query,index)=>query.data?.id===ids[index]?[query.data]:[])
  const failed=queries.filter(query=>query.error&&!(query.error instanceof ApiError&&query.error.status===404))
  return {runs:[...publications,...catalog.filter(run=>!publications.some(item=>item.id===run.id)),...fetched],pending:ids.length>0&&(!enabled||queries.some(query=>query.isPending)),error:failed[0]?.error??null,refetch:()=>{for(const query of failed)void query.refetch()}}
}

export function HistoricalExperimentError({refetch}:{refetch:()=>void}) {
  return <div role="alert" className="notice"><p>대상 게시 조회 실패</p><Button type="button" variant="outline" onClick={refetch}>다시 불러오기</Button></div>
}

const outcomeWords:Record<string,string>={Complete:"완료",Failed:"실패",PASS:"통과",FAIL:"실패",INVALID:"무효",NOT_EVALUATED:"미평가",Executed:"실행",Cancelled:"취소",Invalid:"무효"}
const outcomeReasons:Record<string,string>={IdentityMismatch:"실행 대상 식별자 불일치",NoControlEffects:"복구할 제어 변경 없음",ProcedureComplete:"절차 완료",CleanupFailed:"정리 실패",ExecutionLost:"실행 기록 확인 불가",RemoteEffectUnknown:"원격 변경 결과 미확인"}
const outcomeLabel=(axis:{value:string;reason:string})=>axis.reason==="SLOFailed"?"SLO 미충족":outcomeWords[axis.value]??"미기록"

export function ExperimentOutcome({run,stage}:{run:Experiment;stage?:ExperimentStage}) {
  const outcome=experimentOutcome(run,stage)
  return <dl aria-label={stage?"단계 판정 3축":"실험 판정 3축"} className="flex flex-wrap gap-x-4 gap-y-1 text-sm">{([["execution","실행"],["measurement","측정"],["cleanup","정리"]] as const).map(([axis,label])=><div key={axis} className="flex items-start gap-2"><dt className="text-muted-foreground">{label}</dt><dd className={["Failed","FAIL","INVALID"].includes(outcome[axis].value)?"font-medium text-destructive":""}>{outcomeLabel(outcome[axis])}{outcome[axis].reason&&outcome[axis].reason!=="미기록"&&outcome[axis].reason!=="SLOFailed"&&<div className="text-xs font-normal text-muted-foreground">{outcomeReasons[outcome[axis].reason]??"원문 확인"}</div>}</dd></div>)}</dl>
}

export function ExperimentInjections({run,stage}:{run:Experiment;stage:ExperimentStage}) {
  const rows=injectionRows(run,stage)
  return <section className="space-y-2" aria-label="장애 주입 결과"><h3 className="text-sm font-semibold">장애 주입</h3>{rows.length?<div className="overflow-x-auto"><table className="w-full text-xs tabular-nums"><thead><tr>{["주입 ID","동작","결과","사유","예정 (s)","실제 (s)"].map(label=><th key={label} scope="col" className="p-2 text-left">{label}</th>)}</tr></thead><tbody>{rows.map(row=><tr key={row.id} className="border-t"><th scope="row" className="p-2 text-left font-normal">{row.id}</th><td className="p-2">{row.action==="delete-serving-pod"?"서빙 파드 삭제":row.action}</td><td className="p-2">{outcomeWords[row.outcome]??"미기록"}</td><td className="break-words p-2">{row.reason}</td><td className="p-2">{row.scheduledOffsetSeconds===null?"미기록":formatMeasurementNumber(row.scheduledOffsetSeconds,"s")}</td><td className="p-2">{row.actualOffsetSeconds===null?"미기록":formatMeasurementNumber(row.actualOffsetSeconds,"s")}</td></tr>)}</tbody></table></div>:<span className="text-xs text-muted-foreground">{run.definition?"주입 없음":"미기록"}</span>}</section>
}

export function ExperimentJudgment({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const pairs=definitionComparisons(run,runs),recorded=recordedComparisons(run)
  const word=recorded===undefined?"비교 미기록":!recorded.length?"비교 없음":definitionComparisonReason(run,runs).includes("대상 게시 없음")?"대상 게시 없음":pairs.some(pair=>comparisonValidity(pair.chill,pair.control,pair.leftRun,pair.rightRun).limited)?"참고":"일치"
  return <strong aria-label="비교 상태" className="text-sm font-medium">{word}</strong>
}

export function ExperimentLimits({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const {coverageCounts:c}=campaignScoreboard(run,runs)
  if(!run.definition&&run.comparisons===undefined)return null
  const reasons=[["규칙 다름",c.ruleDifferences],["규칙 미기록",c.ruleMissing],["정의 미기록",c.identityUnknown],["정의 다름",c.identityDifferences],["부하 다름",c.loadDifferences],["부하 미기록",c.loadUnknown],["길이 다름·미확인",c.durationDifferences],["대응 없음",c.unpaired]] as const
  const limited=reasons.filter(([,count])=>count>0)
  if(!limited.length)return null
  return <table aria-label="비교 제한 원인" className="w-full table-fixed text-left text-xs font-normal"><thead><tr><th scope="col" className="pr-3 font-normal">제한 원인</th><th scope="col" className="text-right font-normal">구간 수 (개)</th></tr></thead><tbody>{limited.map(([label,count])=><tr key={label}><th scope="row" className="break-words pr-3 font-normal">{label}</th><td className="text-right tabular-nums">{count}</td></tr>)}</tbody></table>
}

export function ExperimentCoverage({run,runs=[run]}:{run:Experiment;runs?:Experiment[]}) {
  const {coverageCounts:c}=campaignScoreboard(run,runs)
  if(!run.definition&&run.comparisons===undefined)return null
  const groups=[["일치",c.matched],["참고",c.limited],["대응 없음",c.unpaired]] as const
  return <table aria-label="비교 구간 수" className="w-full table-fixed text-left text-xs"><thead><tr><th scope="col" className="font-normal">비교 상태</th><th scope="col" className="text-right font-normal">구간 수 (개)</th></tr></thead><tbody>{groups.map(([label,count])=><tr key={label}><th scope="row" className="break-words font-normal">{label}</th><td className="text-right tabular-nums">{count}</td></tr>)}</tbody></table>
}


export function ExperimentFacts({run}:{run:Experiment}) {
  const facts=run.facts
  const text=(value:unknown)=>typeof value==="string"||typeof value==="number"||typeof value==="bigint"?String(value):"미기록"
  const patchRows=(label:string,patch:Published|undefined):[string,string][]=>{
    if(patch===undefined)return []
    const entries=settingsPatchEntries(patch)
    return entries.length?entries.map(([path,value])=>[`${label} · ${settingsPatchKeyLabel(path)} (${path})`,value===null?"null":publishedText(value)]):[[label,"변경 없음"]]
  }
  const rows:[string,string][]=[
    ["이름",facts?.name??run.definition?.run.name??"미기록"],
    ["질문",facts?.question??run.definition?.spec.question??"미기록"],
    ...patchRows("Run 운영 설정 패치",run.definition?.spec.settingsPatch),
    ...(run.definition?.spec.stages.flatMap(stage=>patchRows(`단계 운영 설정 패치 · ${stage.id}`,stage.settingsPatch))??[]),
    ...(facts?.stages?.length?facts.stages.flatMap(stage=>[
      ["단계 ID",stage.id],
      ["단계 행",stage.row??"미기록"],
      ["단계 정책",definitionPolicy(stage,run)],
      ["변경 설정 키",Array.isArray(stage.settingsPatchKeys)&&stage.settingsPatchKeys.every(key=>typeof key==="string")?stage.settingsPatchKeys.join(", ")||"변경 없음":"미기록"],
      ["부하 시나리오 이름",text(stage.loadScenarioRef?.name)],
      ["부하 시나리오 UID",text(stage.loadScenarioRef?.uid)],
    ] as [string,string][]):[["단계 정책","미기록"] as [string,string]]),
    ...(facts?.sloSources?.length?facts.sloSources.flatMap(source=>[
      ["SLO 출처 이름",source.name],
      ["SLO 출처 UID",source.uid],
      ["SLO 출처 정의",text(source.generation)],
      ["SLO 백분위",text(source.slo?.percentile)],
      ["SLO 목표 지연",typeof source.slo?.targetLatencyNanos==="number"||typeof source.slo?.targetLatencyNanos==="bigint"?formatMeasurementDuration(source.slo.targetLatencyNanos):"미기록"],
    ] as [string,string][]):[["SLO 출처","미기록"] as [string,string]]),
    ...(facts?.loadSources?.length?facts.loadSources.flatMap(source=>[["부하 출처 이름",source.name],["부하 출처 UID",source.uid]] as [string,string][]):[["부하 출처","미기록"] as [string,string]]),
  ]
  return <details className="min-w-0 text-xs font-normal"><summary className="min-h-10 cursor-pointer">질문·출처</summary><table aria-label="기록된 실험 사실" className="w-full table-fixed text-left text-xs [&_th]:break-words [&_td]:[overflow-wrap:anywhere]"><thead><tr><th className="w-1/3 p-1 font-normal">항목</th><th className="p-1 font-normal">값</th></tr></thead><tbody>{rows.map(([label,value],index)=><tr key={index} className="border-t"><th scope="row" className="p-1 font-normal">{label}</th><td className="p-1">{value}</td></tr>)}</tbody></table></details>
}

export function ExperimentMetadata({run,stage}:{run:Experiment;stage:ExperimentStage}) {
  const parts=stageLoadParts(stage,run),rules=object(stage.kpis.measurementRules)
  const loadReference=run.facts?.stages?.find(item=>item.id===stage.id)?.loadScenarioRef
  const loadMissing=!run.definition&&loadReference?"목록 미제공":"게시 기록 없음"
  const number=(value:number)=>value.toLocaleString("ko-KR",{maximumFractionDigits:2})
  const coverage=object(rules.energyCoverage)
  const ruleRows=[
    ["실행기 소스 버전",typeof rules.harnessRepositoryCommit==="string"?rules.harnessRepositoryCommit:"미기록","—"],
    ["전력 결손 비율 상한",typeof coverage.maxGapFraction==="number"&&Number.isFinite(coverage.maxGapFraction)?number(coverage.maxGapFraction*100):"미기록","%"],
    ["단일 전력 공백 상한",typeof coverage.maxSingleGapSeconds==="number"&&Number.isFinite(coverage.maxSingleGapSeconds)?number(coverage.maxSingleGapSeconds):"미기록","s"],
    ["근거 출처",rules.evidenceSource==="api"?"공개 API":rules.evidenceSource==null?"미기록":"원문 확인","—"],
    ["GC 집계 방식",rules.gcMeaning==="runtime-metrics-delta"?"런타임 누적값 차이":rules.gcMeaning==null?"미기록":"원문 확인","—"],
  ]
  return <div className="min-w-0 space-y-2 text-xs">
    <table aria-label="측정 규칙" className="w-full text-left tabular-nums"><thead><tr>{["항목","값","단위"].map(label=><th key={label} scope="col" className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{ruleRows.map(([label,value,unit])=><tr key={label} className="border-t"><th scope="row" className="p-1 font-normal">{label}</th><td className="break-all p-1">{value}</td><td className="p-1">{unit}</td></tr>)}</tbody></table>
    <table aria-label="계획 부하" className="w-full table-fixed text-left tabular-nums [&_td]:[overflow-wrap:anywhere]"><thead><tr>{["구간","서비스","길이 (s)","요청률 (req/s)"].map(label=><th key={label} className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{parts.length?parts.flatMap(part=>Object.entries(part.rates).map(([name,value])=><tr key={`${part.id}/${name}`} className="border-t"><th className="p-1 font-normal">{part.id}</th><td className="break-all p-1">{name}</td><td className="p-1">{number(part.durationSeconds)}</td><td className="p-1">{number(value)}</td></tr>)):<tr><td colSpan={4} title={loadReference?"부하 시나리오 참조만 목록에 제공됩니다. 상세에서 계획 구간을 확인하세요.":undefined}>{loadMissing}</td></tr>}</tbody></table>
    <table aria-label="서비스 식별자·정의" className="w-full text-left tabular-nums"><thead><tr>{["서비스","식별자","정의"].map(label=><th key={label} scope="col" className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{experimentRows(stage).map(row=><tr key={row.source} className="border-t"><th scope="row" className="break-all p-1 font-normal">{row.name}</th><td className="break-all p-1">{publishedText(row.uid)||"미기록"}</td><td className="break-all p-1">{publishedText(row.definition)||"미기록"}</td></tr>)}</tbody></table>
    {!!run.definition?.workloads.length&&<table aria-label="정의 출처 SLO" className="w-full table-fixed text-left tabular-nums [&_td]:[overflow-wrap:anywhere]"><caption className="text-left" title="게시 당시 동결된 서비스 정의입니다. 요청 원값에 서비스 UID가 없으면 단계 적용 SLO로 연결하지 않습니다.">정의 출처 SLO</caption><thead><tr>{["서비스","식별자","세대","분위수","목표 지연 (ms)"].map(label=><th key={label} className="p-1 font-normal">{label}</th>)}</tr></thead><tbody>{run.definition.workloads.map(workload=><tr key={workload.uid} className="border-t"><th className="break-all p-1 font-normal">{workload.name}</th><td className="p-1">{workload.uid}</td><td className="p-1">{String(workload.generation)}</td><td className="p-1">{workload.slo.percentile}</td><td className="p-1">{number(Number(workload.slo.targetLatencyNanos)/1e6)}</td></tr>)}</tbody></table>}
  </div>
}


export function ExperimentResultRow({run,runs=[run],pending=false,error=false,offset=0,service=""}:{run:Experiment;runs?:Experiment[];pending?:boolean;error?:boolean;offset?:number;service?:string}) {
  const recorded=recordedComparisons(run),pairs=definitionComparisons(run,runs)
  const state=error?"조회 실패":pending?"조회 중":recorded===undefined?(run.facts?"상세에서 확인":"비교 미기록"):!recorded.length?"비교 없음":pairs.length?"기록된 비교쌍": "상세에서 확인"
  const policies=[...new Set((run.facts?.stages??run.definition?.spec.stages??run.stages).map(stage=>definitionPolicy(stage,run)))]
  const outcome=experimentOutcome(run)
  return <tr className="border-t">
    <th scope="row" className="max-w-64 break-all p-3 text-left font-medium"><Link prefetch={false} className="inline-flex min-h-11 items-center underline" href={`/experiments/results?${new URLSearchParams({a:run.id,offset:String(offset),...(service?{service}:{})})}`}>{run.facts?.name??run.definition?.run.name??run.id}</Link></th>
    <td data-label="기간 (KST)" className="p-3 text-xs">{formatMeasurementTime(run.from)}<br/>{formatMeasurementTime(run.to)}</td>
    <td data-label="정책" className="max-w-64 break-words p-3">{policies.join(" / ")||"미기록"}<span className="block text-xs text-muted-foreground">{run.stages.length}단계</span></td>
    {(["execution","measurement","cleanup"] as const).map((axis,index)=><td key={axis} data-label={["실행","측정","정리"][index]} className="p-3">{outcomeLabel(outcome[axis])}</td>)}
    <td data-label="비교 상태" className="p-3">{state}</td>
  </tr>
}
