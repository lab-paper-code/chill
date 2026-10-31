"use client"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import { useMeasurementCoverage, useCoverageInventory } from "@/features/measurement-coverage"
import { Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { campaignGroups, campaignScoreboard, experimentHeatmap, type Experiment } from "@/lib/experiments"
import { overviewCoverageMatrix, overviewDeviceLabel } from "@/lib/overview"
import { coverageFailureHref, coveragePathVisible, coverageRequestHref } from "@/lib/measurement-coverage"
import { parseSchedulerJSON } from "@/lib/scheduler"
import "./overview.css"

export function OverviewPage() {
  const {config}=useSession(),params=useSearchParams(),namespace=params.get("namespace")??config.namespace
  const coverage=useMeasurementCoverage(namespace),inventory=useCoverageInventory(),experiments=useChillQuery<{items:Experiment[]}>("v1/experiments",{decoder:parseSchedulerJSON})
  const data=coverage.data,matrix=overviewCoverageMatrix(data?.groups??[])
  const groups=campaignGroups(experiments.data?.items??[]),recent=groups.find(group=>group.pairs.length)??groups[0]
  const summary=recent?campaignScoreboard(recent.run).summary:null,stages=recent?experimentHeatmap(recent.run).filter(row=>row.other):[],maxEnergy=Math.max(1,...stages.map(row=>Math.abs(row.energyPercent??0)))
  const actionGroups=new Map<string,{action:NonNullable<typeof data>["nextActions"][number];cell:NonNullable<typeof data>["cells"][number];reason:string;count:number}>(),seen=new Set<string>()
  for(const action of data?.nextActions??[]) {
    const identity=`${action.cellId}/${action.kind}/${action.reasonCode}`
    if(seen.has(identity))continue
    seen.add(identity)
    const cell=data?.cells.find(cell=>cell.id===action.cellId)
    if(!cell||!coveragePathVisible(cell,inventory.data))continue
    const failure=cell.failureSummary.find(row=>row.requestId===action.entityId)??cell.failureSummary[0]
    const reason=action.kind==="missing-batches"?"배치 부족":/CUDAOutOfMemory|GPU.*Memory|GPU 메모리/i.test(`${failure?.code??""} ${failure?.label??""} ${action.reasonCode}`)?"GPU 메모리 부족":"기타"
    const key=`${action.kind}/${reason}`,group=actionGroups.get(key)
    if(group)group.count++;else actionGroups.set(key,{action,cell,reason,count:1})
  }
  const actions=[...actionGroups.values()].sort((a,b)=>Number(b.action.kind==="failure")-Number(a.action.kind==="failure")),actionCount=actions.reduce((sum,row)=>sum+row.count,0)
  const returnTo=`/?${new URLSearchParams({namespace})}`
  return <Screen title="측정·실험 현황" width="data" actionPlacement="header" action={<Button variant="outline" onClick={()=>{void coverage.refetch();void experiments.refetch()}} disabled={coverage.isFetching||experiments.isFetching}>갱신</Button>}>
    <p className="overview-status" role="status">{namespace} / {coverage.error?"커버리지 조회 실패":!data?"커버리지 수신 대기":data.freshness==="fresh"?"최신 자료":"이전 자료"} / {data?.completeness==="partial"?"일부 확인":""}</p>
    <div className="overview-workspace">
      <section className="overview-panel"><header><h2>모델별 커버리지</h2><Link href={`/measurements?${new URLSearchParams({namespace})}`}>전체 보기</Link></header>
        {matrix.rows.length?<div className="overview-matrix-scroll"><table className="overview-matrix"><caption className="sr-only">사용 가능 / 지원 확인 조합</caption><thead><tr><th scope="col">모델</th>{matrix.devices.map(device=><th scope="col" key={device.uid}>{overviewDeviceLabel(device.name)}</th>)}</tr></thead><tbody>{matrix.rows.map(row=><tr key={row.model.uid}><th scope="row">{row.model.name}</th>{row.cells.map((group,index)=>{
          const supported=group?.supported??null,ratio=supported!==null&&supported>0?Math.min(1,group!.complete/supported):0
          const query=new URLSearchParams({namespace,model:row.model.name,deviceClass:matrix.devices[index].name,...(group?.cellIds[0]?{cell:group.cellIds[0]}:{})})
          return <td key={matrix.devices[index].uid}><Link className={`overview-heat-cell ${ratio===0?"overview-heat-empty":""}`} style={{background:ratio>0?`color-mix(in srgb, var(--primary) ${12+ratio*45}%, var(--background))`:undefined}} href={`/measurements?${query}`} aria-label={`${row.model.name} ${overviewDeviceLabel(matrix.devices[index].name)}: ${group?.complete??0} / ${supported??"미확인"}`}>{group?.complete??0}/{supported??"—"}</Link></td>
        })}</tr>)}</tbody></table></div>:<p>{coverage.error?"커버리지 조회 실패":"커버리지 수신 대기"}</p>}
      </section>
      <section className="overview-panel"><header><h2>{recent?.run.id??"최근 실험 비교"}</h2></header>{recent&&summary?<>
        <table className="overview-chips"><thead><tr><th>에너지 Δ %</th><th>최악 성공률 Δ %p</th><th>최악 p95 배율</th></tr></thead><tbody><tr>{[{value:summary.energyPercent,loss:(summary.energyPercent??0)>0,neutral:summary.energyPercent===0},{value:summary.successDelta,loss:(summary.successDelta??0)<0,neutral:summary.successDelta===0},{value:summary.p95Ratio,loss:(summary.p95Ratio??1)>1,neutral:summary.p95Ratio===1}].map((metric,index)=><td key={index} className={metric.value===null||metric.neutral?"overview-neutral":metric.loss?"overview-loss":"overview-gain"}>{metric.value===null?"—":`${index<2&&metric.value>0?"+":""}${metric.value.toFixed(index===2?2:1)}`}</td>)}</tr></tbody></table>
        <figure className="overview-energy"><figcaption>구간별 에너지 Δ · req/s</figcaption><div className="overview-energy-bars">{stages.map(row=>{const delta=row.energyPercent,width=Math.abs(delta??0)/maxEnergy*48;return <div key={row.id} title={delta===null?"에너지 비교 불가":`${delta.toFixed(2)}%`}><div className="overview-energy-track"><span className={delta!==null&&delta<0?"overview-energy-gain":"overview-energy-loss"} style={{left:`${delta!==null&&delta<0?50-width:50}%`,width:`${width}%`}}/></div><span>{row.rate===null?"—":row.rate.toFixed(0)}</span></div>})}</div></figure>
        <Link href={`/experiments?${new URLSearchParams({a:recent.run.id})}`}>자세히 →</Link>
      </>:<p>{experiments.error?"실험 조회 실패":experiments.isFetching?"실험 수신 대기":"비교 기록 없음"}</p>}</section>
    </div>
    <section className="overview-panel"><header><h2>다음 조치</h2><Link href={`/measurements?${new URLSearchParams({namespace})}`}>전체 {actionCount}건 →</Link></header>{actions.length?<table className="overview-actions"><thead><tr><th aria-label="상태"/><th>대상</th><th>원인</th><th>동작</th></tr></thead><tbody>{actions.slice(0,3).map(({action,cell,reason,count})=>{
      const failure=action.kind==="failure"?cell.failureSummary.find(row=>row.requestId===action.entityId)??cell.failureSummary[0]:undefined
      const href=failure?coverageFailureHref(failure,namespace,returnTo):coverageRequestHref(cell,namespace,returnTo,action.variantId??undefined)
      return <tr key={`${action.kind}/${reason}`}><td aria-label={action.kind==="failure"?"실패":"확인 필요"}>{action.kind==="failure"?"×":"△"}</td><td title={count>1?"같은 원인의 대표 대상":""}>{cell.model.name} · {overviewDeviceLabel(cell.deviceClass.name)} · {cell.powerMode} · {cell.executionPath.replace(/^firstfloor-/,"")}</td><td>{reason}{count>1&&<> {count}</>}</td><td><Link href={href}>{failure?.runName?"회차 열기":failure?"요청 열기":"요청"}</Link></td></tr>
    })}</tbody></table>:<p>{data?"추가 조치 없음":"조치 확인 대기"}</p>}</section>
  </Screen>
}
