"use client"

import { useChillQuery } from "@/lib/client"
import type { CurvePlanSpec, ProfilesView, SchedulerView } from "@/lib/read-types"
import { explain } from "@/lib/labels"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { QueryGate, SourceJSON, StatusBadge } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"

// The four measurement conditions of a profile (CurvePlanSpec), in plain words. Raw values stay in the 원본 view.
export const conditionRows = (condition:CurvePlanSpec):[string,string][] => [
  ["모델 파일", `${condition.logicalModel} / ${condition.artifact.name}`],
  ["실행 방식", [condition.runtimeFamily, condition.backend].map(code => explain(code)[0]).join(" · ")],
  ["전처리기", condition.preprocessor.name || "없음"],
  ["데이터셋", condition.activeProtocol.dataset?.name ?? "없음"],
]

// /scheduler/profiles: the latest completed measurement per condition, which the operating judgment uses.
export function SchedulerProfilesPage() {
  const query = useChillQuery<ProfilesView>("v1/scheduler/profiles", {decoder:parseSchedulerJSON})
  const scheduler = useChillQuery<SchedulerView>("v1/scheduler", {decoder:parseSchedulerJSON})
  const profiles = query.data?.profiles ?? [], issues = query.data?.insufficiencies ?? [], costIssues = scheduler.data?.state.costIssues ?? []
  const snapshot = scheduler.data?.state.snapshot, names = new Map((snapshot?.workloads ?? []).flatMap(item => item ? [[item.uid, item.name] as const] : []))
  const count = scheduler.data?.settings.autonomousWorkloads?.length
  return <Screen back={["/scheduler","운영"]} status={count === undefined ? undefined : <StatusBadge tone={count ? "info" : "idle"}>{count ? `자동 제어 서비스 ${count}개` : "자동 제어 서비스 없음"}</StatusBadge>}
    title={!query.data ? "측정 프로파일" : profiles.length ? `운영 판단이 측정 프로파일 ${profiles.length}개를 쓰고 있어요` : "아직 운영 판단에 쓸 측정 프로파일이 없어요"}
    lede="끝난 측정 가운데 조건마다 가장 최근 결과를 운영 판단이 써요. 자동 제어가 꺼진 서비스의 구성은 바뀌지 않아요.">
    <QueryGate query={query}>
      {profiles.length ? <Rows label="측정 프로파일">{profiles.map(profile => {
        const fit = [...new Set((snapshot?.options ?? []).filter(option => option.instance.profile?.id === profile.id).flatMap(option => (option.compatibleUIDs ?? []).map(uid => names.get(uid) ?? "이전 등록 서비스")))]
        // A profile ID is namespace/name/uid of its measurement.
        const [namespace, name] = profile.id.split("/"), hint = fit.length ? fit.join(", ") : "맞는 서비스가 없어요"
        return <RowDialog key={profile.id} label={`${profile.condition.logicalModel} · ${profile.nodeName}`} hint={hint} title={profile.id}>
          <Facts label="측정 조건" items={[["서비스", hint], ["장비", profile.nodeName], ...conditionRows(profile.condition)]}/>
          <SourceJSON inline value={profile.condition} title="측정 조건 원본 보기"/>
          <Rows><Row href={`/measurements/batch-energy-optimizations/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`} label="측정 결과 보기"/></Rows>
        </RowDialog>
      })}</Rows> : query.data && <Empty title="측정이 끝나면 여기에 나타나요"/>}
      {issues.length > 0 && <Rows label="프로파일이 되지 못한 측정">{issues.map((issue, index) => <RowDialog key={index} label={issue.source.Name} hint={explain(issue.code)[0]} title={issue.source.Name}>
        <p>{explain(issue.code).join(" ")}</p><p className="text-sm text-muted-foreground">{issue.reason}</p><SourceJSON value={issue} title="원본 자료 보기"/>
      </RowDialog>)}</Rows>}
    </QueryGate>
    {costIssues.length > 0 && <Rows label="비용 근거가 되지 못한 측정">{costIssues.map((issue, index) => <RowDialog key={index} label={issue.source} hint={explain(issue.code)[0]} title={issue.source}>
      <p>{explain(issue.code).join(" ")}</p><p className="text-sm text-muted-foreground">{issue.reason}</p>
    </RowDialog>)}</Rows>}
    {query.data && <Rows label="더 보기"><RowDialog label="원본 자료" title="측정 프로파일 원본"><SourceJSON inline value={query.data}/></RowDialog></Rows>}
  </Screen>
}
