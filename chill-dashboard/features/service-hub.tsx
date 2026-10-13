"use client"

import * as React from "react"
import Link from "next/link"
import { useChillQuery, type ApiError } from "@/lib/client"
import type { ObservationResult, PoolsView, SchedulerView } from "@/lib/read-types"
import { definitionIdentity, resourceHref, sloTarget, type WorkloadView } from "@/lib/domain"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { ReadRegion, SourceExport, SourceJSON, StatusBadge, statusLabel, type Tone } from "@/components/read-region"
import { Button } from "@/components/ui/button"

type Query = {data?:unknown;error:ApiError|null;isPending:boolean;isFetching:boolean;dataUpdatedAt:number;refetch:()=>unknown}
type Stage = {id:string;label:string;tone:Tone;status:string}
const outcomeLabels:Record<string,string> = {completed:"완료",error:"오류",rejected:"접수 거부",timeout:"시간 초과",canceled:"취소",incomplete:"미완료",delivery_failed:"전달 실패"}
const lastHour = () => { const to = new Date(); return {from:new Date(to.getTime() - 3600000).toISOString(), to:to.toISOString()} }
const utc = (at:string) => `${new Date(at).toLocaleString("ko-KR",{timeZone:"UTC",dateStyle:"medium",timeStyle:"short"})} UTC`

// One service, one page: definition → evidence → placement → admission → operation, each from its owner response.
export function ServiceHub({view,query,actions}:{view:WorkloadView;query:Query;actions:React.ReactNode}) {
  const {name, uid} = view.desired.metadata
  const [range,setRange] = React.useState(lastHour)
  // Sections mount with the data, after Next has already handled the hash.
  React.useEffect(() => { const id = window.location.hash.slice(1); if (id) document.getElementById(id)?.scrollIntoView() }, [])
  const pools = useChillQuery<PoolsView>("v1/pools")
  const scheduler = useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON})
  const observations = useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(uid)}?${new URLSearchParams({...range,limit:"20"})}`,{decoder:parseSchedulerJSON})
  const instances = (pools.data?.items ?? []).flatMap(pool => pool.instances ?? []).filter(instance => instance.workloads?.some(workload => workload.uid === uid))
  const applied = view.appliedDefinition
  const cohort = applied ? observations.data?.cohorts?.find(row => row.definition === definitionIdentity(applied)) : undefined
  const operating = view.operation && !["Completed","Failed"].includes(view.operation.phase)
  const stages:Stage[] = [
    {id:"definition",label:"정의",...(view.operation?.phase === "Failed" ? {tone:"fail",status:"변경 실패"} : operating ? {tone:"info",status:"적용 중"} : applied ? {tone:"ok",status:"적용됨"} : {tone:"info",status:"적용 대기"})},
    {id:"evidence",label:"측정 근거",...(view.supportedInputs.length ? {tone:"ok",status:`입력 ${view.supportedInputs.length}개`} : {tone:"warn",status:"측정 필요"})},
    {id:"placement",label:"배치",...(!pools.data ? {tone:"idle",status:pools.error ? "확인 불가" : "확인 중"} : !instances.length ? {tone:"warn",status:"배치 없음"} : instances.some(instance => instance.ready) ? {tone:"ok",status:`인스턴스 ${instances.length}개`} : {tone:"warn",status:"준비 중"})},
    {id:"admission",label:"요청 접수",...(view.admission ? {tone:"ok",status:"접수 중"} : {tone:"warn",status:statusLabel(view.reason || view.readiness)})},
    {id:"operation",label:"응답 목표",...(!observations.data ? {tone:"idle",status:observations.error ? "확인 불가" : "확인 중"} : !cohort?.sampleCount || cohort.quantileNanos == null || cohort.targetLatencyNanos == null ? {tone:"idle",status:"표본 없음"} : Number(cohort.quantileNanos) <= Number(cohort.targetLatencyNanos) ? {tone:"ok",status:"목표 안"} : {tone:"warn",status:"목표 초과"})},
  ]
  return <>
    <ReadRegion id="admission" title="지금 상태" query={query}>
      <div className="grid gap-5">
        <div className="flex flex-wrap items-center gap-3"><p className="text-xl font-semibold">{view.admission ? "추론을 시작할 수 있어요" : "아직 추론을 시작할 수 없어요"}</p><StatusBadge value={view.admission ? "Ready" : view.reason || view.readiness}>{view.admission ? "요청 접수 중" : undefined}</StatusBadge></div>
        <p className="text-muted-foreground"><Link href={`/models/${encodeURIComponent(view.desired.spec.modelRef.name)}`}>{view.desired.spec.modelRef.name}</Link> · {sloTarget(view.desired.spec)}</p>
        <ol className="lifecycle" aria-label="서비스 준비 단계">{stages.map(stage => <li key={stage.id} data-tone={stage.tone}><a href={`#${stage.id}`}><span className="lifecycle-label">{stage.label}</span><StatusBadge tone={stage.tone}>{stage.status}</StatusBadge></a></li>)}</ol>
        <div className="flex flex-wrap gap-3">{actions}</div>
      </div>
    </ReadRegion>

    <section className="read-region" id="definition" aria-label="정의와 적용">
      <div className="read-region-heading"><h2>정의와 적용</h2><span className="read-region-meta">관측 {utc(view.observedAt)}</span></div>
      <dl className="facts">
        <div><dt>설정한 모델과 응답 목표</dt><dd>{view.desired.spec.modelRef.name} · {sloTarget(view.desired.spec)}</dd></div>
        <div><dt>현재 적용된 모델과 응답 목표</dt><dd>{applied ? <><Link href={resourceHref("models", applied.model.name, applied.model.uid)}>{applied.model.name}</Link> · {applied.slo.percentile}%의 요청을 {applied.slo.targetLatencyNanos / 1000000}밀리초 안에 처리</> : "아직 적용되지 않았어요"}</dd></div>
        <div><dt>설정 변경 진행</dt><dd>{view.operation ? `${statusLabel(view.operation.kind)} · ${statusLabel(view.operation.phase)}` : "진행 중인 변경 없음"}</dd></div>
        <div><dt>처리 중인 요청</dt><dd>배정 대기 {view.pending}건 · 배정됨 {view.assigned}건</dd></div>
      </dl>
      {view.operation?.error && <p role="alert">{view.operation.error}</p>}
      <div className="flex flex-wrap gap-2">{view.operation && <SourceJSON title="변경 작업 기록" value={view.operation} />}<SourceJSON title="전체 상태와 관측 시각" value={view} /><SourceExport value={view} filename={`workload-${name}.json`} /></div>
    </section>

    <section className="read-region" id="evidence" aria-label="측정 근거">
      <div className="read-region-heading"><h2>측정 근거</h2><Link href="/measurements" className="text-sm">측정 결과에서 찾기</Link></div>
      {view.supportedInputs.length ? <ul className="grid gap-3">{view.supportedInputs.map((input, index) => <li key={index} className="grid gap-2 rounded-xl border p-4">
        <p className="flex flex-wrap items-center gap-2"><StatusBadge tone="ok">측정 완료</StatusBadge><span className="font-semibold">{input.executionPath}</span><span className="text-muted-foreground">{input.artifact}</span></p>
        <p className="text-sm">{input.tensors.map(t => `${t.name} · ${t.datatype} · ${t.sampleShape.join(" × ")}`).join(" / ")}</p>
        <p className="text-sm text-muted-foreground">연결된 측정 {input.profiles.length}개</p>
        <SourceJSON title="입력과 측정 근거" value={input} />
      </li>)}</ul> : <p>이 모델의 실행 조건에 맞는 측정 결과가 필요해요. 파일이나 실행 방식을 등록하는 것만으로 추론을 시작할 수는 없어요.</p>}
      {applied?.model.paths.filter(path => !view.supportedInputs.some(input => input.executionPath === path.name)).map(path => <p key={path.name} className="flex flex-wrap items-center gap-2"><StatusBadge tone="warn">측정 필요</StatusBadge><span className="font-medium">{path.name}</span><span className="text-muted-foreground">{path.artifact} · {path.runtimeFamily} / {path.backend}</span></p>)}
    </section>

    <ReadRegion id="placement" title="배치" query={pools}>{pools.data && <div className="grid gap-3">
      {instances.length ? <div className="overflow-x-auto rounded-xl border"><table><thead><tr><th>인스턴스</th><th>노드 · 실행 그룹</th><th>상태</th><th>이 서비스의 대기 / 실행</th><th>요청 분배 가중치</th></tr></thead><tbody>{instances.map(instance => <tr key={instance.id}>
        <td className="break-all">{instance.id}</td>
        <td>{instance.node.name} · <Link href={`/pools/${encodeURIComponent(instance.pool)}`}>{instance.pool}</Link></td>
        <td><StatusBadge tone={instance.ready ? "ok" : "warn"}>{instance.ready ? "준비됨" : instance.reason ? statusLabel(instance.reason) : "준비 중"}</StatusBadge></td>
        <td>{instance.waiting?.[uid] ?? 0} / {instance.running?.[uid] ?? 0}</td>
        <td>{scheduler.data?.state.snapshot.routing?.[uid]?.[instance.id] ?? (scheduler.error ? "확인 불가" : "제공 없음")}</td>
      </tr>)}</tbody></table></div> : <p>이 서비스를 실행하는 인스턴스가 아직 없어요. 운영 판단이 배치를 고르면 여기에 나타나요.</p>}
      <p className="text-sm text-muted-foreground">가중치는 원래 값이며 확률이 아니에요. <Link href="/scheduler#services">운영 판단의 예상 보기</Link></p>
    </div>}</ReadRegion>

    <ReadRegion id="operation" title="최근 1시간 운영" query={observations}>{observations.data && <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3"><p className="text-sm text-muted-foreground">{utc(observations.data.from)} – {utc(observations.data.to)} · 전체 {observations.data.total}건</p><Button variant="outline" size="sm" onClick={() => setRange(lastHour())}>지금 기준으로 다시 보기</Button></div>
      {observations.data.cohorts?.length ? <div className="overflow-x-auto rounded-xl border"><table><thead><tr><th>적용 설정</th><th>지연 표본</th><th>관측 지연</th><th>목표</th><th>판정</th></tr></thead><tbody>{observations.data.cohorts.map(row => {
        const met = row.quantileNanos != null && row.targetLatencyNanos != null && Number(row.quantileNanos) <= Number(row.targetLatencyNanos)
        return <tr key={row.definition}><td>{applied && row.definition === definitionIdentity(applied) ? "현재 적용 설정" : "이전 적용 설정"}</td><td>{row.sampleCount}건</td><td>{row.quantileNanos == null ? "값 없음" : `${row.percentile ?? "?"}% · ${nanosToMilliseconds(row.quantileNanos)} ms`}</td><td>{row.targetLatencyNanos == null ? "값 없음" : `${nanosToMilliseconds(row.targetLatencyNanos)} ms`}</td><td>{row.quantileNanos == null || row.targetLatencyNanos == null ? <StatusBadge tone="idle">표본 없음</StatusBadge> : met ? <StatusBadge tone="ok">목표 안</StatusBadge> : <StatusBadge tone="warn">목표 초과</StatusBadge>}</td></tr>
      })}</tbody></table></div> : <p>이 구간에 기록된 요청이 없어요.</p>}
      {Object.keys(observations.data.outcomes ?? {}).length > 0 && <p className="flex flex-wrap gap-2">{Object.entries(observations.data.outcomes).map(([key, value]) => <StatusBadge key={key} value={key}>{outcomeLabels[key] ?? key} {value}건</StatusBadge>)}</p>}
      {!!observations.data.incompleteWindows?.length && <p role="alert">일부 기간의 기록이 불완전해요.</p>}
      <Link href={`/observations?${new URLSearchParams({uid,from:observations.data.from,to:observations.data.to})}`}>이 구간의 요청 기록 전체 보기</Link>
    </div>}</ReadRegion>

    <section className="read-region" id="history" aria-label="최근 변경">
      <div className="read-region-heading"><h2>최근 변경</h2>{observations.data && <Link href={`/changes?${new URLSearchParams({uid,from:observations.data.from,to:observations.data.to})}`} className="text-sm">변경 이력 전체 보기</Link>}</div>
      {observations.data ? observations.data.management?.length ? <ol className="timeline">{[...observations.data.management].sort((a, b) => b.at.localeCompare(a.at)).slice(0, 5).map((event, index) => <li key={`${event.at}/${index}`}>
        <p className="flex flex-wrap items-center gap-2"><StatusBadge value={event.phase} /><span className="font-semibold">{statusLabel(event.kind)}</span><span className="text-sm text-muted-foreground">{utc(event.at)}</span></p>
        <p className="text-sm text-muted-foreground">{event.actorUsername?.startsWith("system:serviceaccount:") ? event.actorUsername.split(":").at(-1) : event.actorUsername ?? "담당자 기록 없음"}{event.error ? ` · ${event.error}` : ""}</p>
      </li>)}</ol> : <p>최근 1시간 동안 변경이 없어요.</p> : <p className="text-sm text-muted-foreground">{observations.error ? "변경 기록을 확인하지 못했어요." : "확인하고 있어요."}</p>}
    </section>
  </>
}
