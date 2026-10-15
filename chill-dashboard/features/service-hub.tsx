"use client"

import * as React from "react"
import Link from "next/link"
import { useChillQuery, type ApiError } from "@/lib/client"
import type { ObservationCohort, ObservationResult, PoolsView, SchedulerView, WireInteger } from "@/lib/read-types"
import { definitionIdentity, resourceHref, sloTarget, type WorkloadView } from "@/lib/domain"
import { nanosToMilliseconds, parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceExport, SourceJSON, StatusBadge, statusLabel } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"

// The five screens under one service, in the order a service becomes usable.
export const serviceSections = {
  definition: ["정의와 적용", "설정한 모델과 지금 적용된 설정"],
  evidence: ["측정 근거", "추론에 쓸 수 있는 입력"],
  placement: ["배치", "이 서비스를 실행하는 곳"],
  operation: ["최근 운영", "최근 1시간 응답 속도"],
  changes: ["변경 기록", "최근 1시간 설정 변경"],
} as const
export type ServiceSectionId = keyof typeof serviceSections

const outcomeLabels:Record<string,string> = {completed:"완료",error:"오류",rejected:"접수 거부",timeout:"시간 초과",canceled:"취소",incomplete:"미완료",delivery_failed:"전달 실패"}
const lastHour = () => { const to = new Date(); return {from:new Date(to.getTime() - 3600000).toISOString(), to:to.toISOString()} }
const localTime = (at:string) => new Date(at).toLocaleString("ko-KR",{dateStyle:"medium",timeStyle:"short"})
const ms = (nanos:WireInteger) => `${nanosToMilliseconds(nanos)}밀리초`
const met = (row?:ObservationCohort) => !row?.sampleCount || row.quantileNanos == null || row.targetLatencyNanos == null ? undefined : Number(row.quantileNanos) <= Number(row.targetLatencyNanos)
const actor = (name?:string) => name?.startsWith("system:serviceaccount:") ? name.split(":").at(-1) : name ?? "담당자 기록 없음"

// Each signal comes from its owner response; the service screen shows one short value per section.
export function useServiceSignals(view?:WorkloadView) {
  const uid = view?.desired.metadata.uid ?? ""
  const [range,setRange] = React.useState(lastHour)
  const pools = useChillQuery<PoolsView>("v1/pools")
  const scheduler = useChillQuery<SchedulerView>("v1/scheduler",{decoder:parseSchedulerJSON})
  const observations = useChillQuery<ObservationResult>(`v1/observations/workloads/${encodeURIComponent(uid)}?${new URLSearchParams({...range,limit:"20"})}`,{decoder:parseSchedulerJSON,enabled:!!uid})
  const instances = (pools.data?.items ?? []).flatMap(pool => pool.instances ?? []).filter(instance => instance.workloads?.some(workload => workload.uid === uid))
  const applied = view?.appliedDefinition
  const cohort = applied ? observations.data?.cohorts?.find(row => row.definition === definitionIdentity(applied)) : undefined
  const op = view?.operation
  const operating = !!op && !["Completed","Failed"].includes(op.phase)
  const unread = (query:{error:ApiError|null}) => query.error ? "확인 불가" : "확인 중"
  const verdict = met(cohort)
  const values:Record<ServiceSectionId,string> = {
    definition: op?.phase === "Failed" ? "변경 실패" : operating ? "적용 중" : applied ? "적용됨" : "적용 대기",
    evidence: view?.supportedInputs.length ? `입력 ${view.supportedInputs.length}개` : "측정 필요",
    placement: !pools.data ? unread(pools) : !instances.length ? "배치 없음" : instances.some(instance => instance.ready) ? `${instances.length}곳` : "준비 중",
    operation: !observations.data ? unread(observations) : verdict === undefined ? "표본 없음" : verdict ? "목표 안" : "목표 초과",
    changes: !observations.data ? unread(observations) : observations.data.management?.length ? `${observations.data.management.length}건` : "없음",
  }
  return {setRange,pools,scheduler,observations,instances,cohort,operating,values}
}
type Signals = ReturnType<typeof useServiceSignals>
type Part = {title:string;lede?:React.ReactNode;body?:React.ReactNode;action?:React.ReactNode}

const sections:Record<ServiceSectionId,(view:WorkloadView,s:Signals)=>Part> = {
  definition(view, s) {
    const {desired, appliedDefinition:applied, operation:op} = view
    const failed = op?.phase === "Failed"
    return {
      title: failed ? "설정 변경이 실패했어요" : s.operating ? "설정을 적용하고 있어요" : applied ? "설정이 적용됐어요" : "아직 설정이 적용되지 않았어요",
      lede: failed ? "변경 작업 기록에서 이유를 보고 설정을 다시 수정해 주세요." : "설정한 내용과 지금 적용된 내용을 비교해요.",
      body: <>
        <Facts items={[
          ["설정한 모델", desired.spec.modelRef.name],
          ["설정한 전처리기", desired.spec.preprocessorRef.name],
          ["설정한 데이터셋", desired.spec.datasetRef?.name ?? "없어요. 측정할 데이터셋을 정해야 요청을 받아요"],
          ["설정한 응답 목표", sloTarget(desired.spec)],
          ["적용된 모델", applied?.model.name ?? "아직 없어요"],
          ["적용된 전처리기", applied?.preprocessor.name ?? "아직 없어요"],
          ["적용된 응답 목표", applied ? `${applied.slo.percentile}%의 요청을 ${applied.slo.targetLatencyNanos / 1000000}밀리초 안에 처리` : "아직 없어요"],
          ["설정 변경", op ? `${statusLabel(op.kind)} · ${statusLabel(op.phase)}` : "진행 중인 변경 없음"],
          ["처리 중인 요청", `배정 대기 ${view.pending}건 · 배정됨 ${view.assigned}건`],
        ]} />
        <Rows>
          <Row href={applied ? resourceHref("models", applied.model.name, applied.model.uid) : `/models/${encodeURIComponent(desired.spec.modelRef.name)}`} label="모델 보기" />
          <Row href={applied ? resourceHref("preprocessors", applied.preprocessor.name, applied.preprocessor.uid) : `/preprocessors/${encodeURIComponent(desired.spec.preprocessorRef.name)}`} label="전처리기 보기" />
          {desired.spec.datasetRef && <Row href={applied?.dataset ? resourceHref("datasets", applied.dataset.name, applied.dataset.uid) : `/datasets/${encodeURIComponent(desired.spec.datasetRef.name)}`} label="데이터셋 보기" />}
          {op && <RowDialog label="변경 작업 기록" value={statusLabel(op.phase)} title="변경 작업 기록">
            <Facts items={[["작업", statusLabel(op.kind)], ["접수", localTime(op.acceptedAt)], ["끝남", op.completedAt ? localTime(op.completedAt) : "아직이에요"], ["요청한 사람", actor(op.actorUsername)]]} />
            {op.error && <p role="alert">{op.error}</p>}
            <SourceJSON inline title="변경 작업 원본" value={op} />
          </RowDialog>}
          <RowDialog label="원본 자료 보기" hint={`관측 ${localTime(view.observedAt)}`} title="전체 상태와 관측 시각"><SourceJSON inline value={view} /><SourceExport value={view} filename={`workload-${desired.metadata.name}.json`} /></RowDialog>
        </Rows>
      </>,
      action: failed ? <Button asChild><Link href={resourceHref("workloads", desired.metadata.name, desired.metadata.uid, "edit")}>설정 수정</Link></Button> : undefined,
    }
  },
  evidence(view) {
    const inputs = view.supportedInputs, preprocessor = view.appliedDefinition?.preprocessor
    const missing = view.appliedDefinition?.model.paths.filter(path => !inputs.some(input => input.executionPath === path.name)) ?? []
    return {
      title: inputs.length ? `측정이 끝난 입력이 ${inputs.length}개 있어요` : "측정 결과가 필요해요",
      lede: "측정이 끝난 실행 방식만 추론에 쓸 수 있어요. 파일이나 실행 방식을 등록하는 것만으로는 시작할 수 없어요.",
      body: inputs.length || missing.length ? <Rows>
        {inputs.map((input, index) => <RowDialog key={index} label={input.executionPath} hint={input.artifact} value="측정 완료" title={`${input.executionPath} 입력과 측정 근거`}>
          <Facts items={[
            ...(preprocessor?.input ?? []).map((f): [string, string] => [`입력 ${f.name}`, `BYTES · ${f.format} · 최대 ${f.maxBytes}바이트`]),
            ...(preprocessor?.output ?? []).map((f): [string, string] => [`출력 ${f.name}`, `BYTES · ${f.format}`]),
            ["전처리기", preprocessor?.name ?? "아직 없어요"], ["연결된 측정", `${input.profiles.length}개`],
          ]} />
          <SourceJSON inline title="입력과 측정 근거" value={input} />
        </RowDialog>)}
        {missing.map(path => <Row key={path.name} href="/measurements" label={path.name} hint={path.artifact} value="측정 필요" />)}
      </Rows> : <Empty title="아직 측정이 끝난 입력이 없어요" />,
      action: <Button asChild variant={inputs.length ? "outline" : "default"}><Link href="/measurements">측정 결과 찾기</Link></Button>,
    }
  },
  placement(view, s) {
    const uid = view.desired.metadata.uid
    const ready = s.instances.filter(instance => instance.ready).length
    return {
      title: !s.pools.data ? "배치" : !s.instances.length ? "아직 이 서비스를 실행하는 곳이 없어요" : ready ? `${ready}곳에서 실행하고 있어요` : "실행을 준비하고 있어요",
      lede: s.instances.length ? "실행하는 곳마다 대기와 처리 현황을 볼 수 있어요." : "운영 판단이 배치를 고르면 여기에 나타나요.",
      body: <QueryGate query={s.pools}><Rows>
        {s.instances.map(instance => {
          const weight = s.scheduler.data?.state.snapshot.routing?.[uid]?.[instance.id]
          return <RowDialog key={instance.id} label={instance.node.name} hint={`실행 그룹 ${instance.pool}`} value={<StatusBadge tone={instance.ready ? "ok" : "warn"}>{instance.ready ? "준비됨" : instance.reason ? statusLabel(instance.reason) : "준비 중"}</StatusBadge>} title={`${instance.node.name}의 실행 현황`}>
            <Facts items={[["이 서비스의 대기", `${instance.waiting?.[uid] ?? 0}건`], ["이 서비스의 실행", `${instance.running?.[uid] ?? 0}건`], ["요청 분배 가중치", weight ?? (s.scheduler.error ? "확인 불가" : "제공 없음")], ["인스턴스", instance.id]]} />
            <p className="text-sm text-muted-foreground">가중치는 원래 값이며 확률이 아니에요.</p>
            <Link href={`/pools/${encodeURIComponent(instance.pool)}`}>실행 그룹 보기</Link>
            <SourceJSON inline value={instance} />
          </RowDialog>
        })}
        <Row href="/scheduler/estimates" label="운영 판단의 예상 보기" />
      </Rows></QueryGate>,
    }
  },
  operation(view, s) {
    const data = s.observations.data
    const applied = view.appliedDefinition
    const verdict = met(s.cohort)
    return {
      title: !data ? "최근 운영" : !data.total ? "이 구간에 기록된 요청이 없어요" : verdict === undefined ? "아직 판단할 표본이 없어요" : verdict ? "응답 목표를 지키고 있어요" : "응답 목표를 넘었어요",
      lede: data && `${localTime(data.from)} – ${localTime(data.to)} · 전체 ${data.total}건${data.incompleteWindows?.length ? " · 일부 기간의 기록이 불완전해요" : ""}`,
      body: <QueryGate query={s.observations}>{data && <>
        {!!data.cohorts?.length && <Rows label="적용 설정별 응답 속도">{data.cohorts.map(row => {
          const ok = met(row)
          return <RowDialog key={row.definition} label={applied && row.definition === definitionIdentity(applied) ? "현재 적용 설정" : "이전 적용 설정"} hint={row.quantileNanos == null ? "값 없음" : `${row.percentile ?? "?"}% 응답 ${ms(row.quantileNanos)}${row.targetLatencyNanos == null ? "" : ` · 목표 ${ms(row.targetLatencyNanos)}`}`} value={<StatusBadge tone={ok === undefined ? "idle" : ok ? "ok" : "warn"}>{ok === undefined ? "표본 없음" : ok ? "목표 안" : "목표 초과"}</StatusBadge>} title="적용 설정별 응답 속도">
            <Facts items={[["요청", `${row.total}건`], ["지연 표본", `${row.sampleCount}건`], ["관측 지연", row.quantileNanos == null ? "값 없음" : `${row.percentile ?? "?"}% · ${ms(row.quantileNanos)}`], ["목표", row.targetLatencyNanos == null ? "값 없음" : ms(row.targetLatencyNanos)]]} />
            <SourceJSON inline value={row} />
          </RowDialog>
        })}</Rows>}
        <Rows>
          {Object.keys(data.outcomes ?? {}).length > 0 && <RowDialog label="처리 결과" value={`${data.total}건`} title="처리 결과"><Facts items={Object.entries(data.outcomes).map(([key, value]):[string,string] => [outcomeLabels[key] ?? statusLabel(key), `${value}건`])} /></RowDialog>}
          <Row href={`/observations?${new URLSearchParams({uid:view.desired.metadata.uid,from:data.from,to:data.to})}`} label="이 구간의 요청 기록 전체 보기" />
        </Rows>
      </>}</QueryGate>,
      action: <Button variant="outline" onClick={() => s.setRange(lastHour())}>지금 기준으로 다시 보기</Button>,
    }
  },
  changes(view, s) {
    const data = s.observations.data
    const events = [...(data?.management ?? [])].sort((a, b) => b.at.localeCompare(a.at))
    return {
      title: !data ? "변경 기록" : events.length ? `최근 1시간 동안 변경이 ${events.length}번 있었어요` : "최근 1시간 동안 변경이 없어요",
      lede: data && `${localTime(data.from)} – ${localTime(data.to)}`,
      body: <QueryGate query={s.observations}>{data && <Rows>
        {events.slice(0, 5).map((event, index) => <RowDialog key={`${event.at}/${index}`} label={statusLabel(event.kind)} hint={`${localTime(event.at)} · ${actor(event.actorUsername)}`} value={<StatusBadge value={event.phase} />} title={`${statusLabel(event.kind)} 기록`}>
          {event.error && <p role="alert">{event.error}</p>}
          <SourceJSON inline title="변경 전후 원본" value={event} />
        </RowDialog>)}
        <Row href={`/changes?${new URLSearchParams({uid:view.desired.metadata.uid,from:data.from,to:data.to})}`} label="변경 이력 전체 보기" />
      </Rows>}</QueryGate>,
    }
  },
}

// One section of one service per screen; dependent detail opens in dialogs.
export function ServiceSection({name,uid,query,section}:{name:string;uid:string;query:ReturnType<typeof useChillQuery<WorkloadView>>;section:ServiceSectionId}) {
  const view = [401,403].includes(query.error?.status ?? 0) ? undefined : query.data
  const s = useServiceSignals(view)
  const part:Part = view ? sections[section](view, s) : {title:serviceSections[section][0]}
  return <Screen back={[resourceHref("workloads", name, view?.desired.metadata.uid ?? uid), name]} title={part.title} lede={part.lede} action={part.action}>
    <QueryGate query={query}>{part.body}</QueryGate>
  </Screen>
}
