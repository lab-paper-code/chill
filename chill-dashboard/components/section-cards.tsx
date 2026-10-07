"use client"

import { useChillQuery, useSession } from "@/lib/client"
import { ReadRegion, SourceJSON, DetailsDialog } from "@/components/read-region"
import type { SystemsView } from "@/lib/read-types"

export function SectionCards({poll=false}:{poll?:boolean}) {
  const {config} = useSession()
  const query = useChillQuery<SystemsView>(`v1/systems?limit=${config.pageSize}&offset=0`,{poll})
  return <ReadRegion title="시스템 상태" query={query}>{query.data && <>{query.data.systems.length ? query.data.systems.map(row => {
    const ready=row.status.conditions?.find(condition=>condition.type==="Ready")
    return <section key={row.uid} className="space-y-4"><h2 className="text-xl font-semibold">{row.name}</h2><p className="text-2xl font-semibold">{ready?.status==="True" ? "준비됨" : ready?.status==="False" ? "준비되지 않음" : "준비 상태 확인 안 됨"}</p><DetailsDialog label="상태 자세히 보기" title={`${row.name} 상태`}><p>시스템의 준비 상태입니다. 장비의 실행 가능 여부나 서비스의 요청 접수 여부와는 다릅니다.</p><SourceJSON title="원본 상태와 조건" value={row} /></DetailsDialog></section>
  }) : <p>조회된 시스템이 없습니다.</p>}<SourceJSON value={query.data}/></>}</ReadRegion>
}
