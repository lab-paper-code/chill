"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { InventoryView, PoolState, PoolsView, PowerModeActuator } from "@/lib/read-types"
import { QueryGate, SourceJSON, SourceExport, StatusBadge } from "@/components/read-region"
import { Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"

const back:[string,string]=["/scheduler","운영 판단"]

// api/v1alpha1/deviceclass_types.go: how ChillServer applies one declared power mode. Shared with the measurement request.
export function powerModeText(actuator:PowerModeActuator) {
  switch(actuator.kind){
    case "Fixed": return "장비 기본 상태 그대로 써요"
    case "JetsonNVPModel": return `Jetson 전력 모드 ${actuator.jetsonNVPModel.id}번으로 바꿔요`
    case "NvidiaLockedGPUClock": {const clock=actuator.nvidiaLockedGPUClock;return `GPU 클럭을 ${clock.minMHz}~${clock.maxMHz} MHz로 고정해요${clock.mode===undefined?"":` (고정 방식 ${clock.mode})`}`}
  }
}

export function ResourcesPage() {
  const params=useSearchParams(),router=useRouter(),{config}=useSession()
  const allowed=new URLSearchParams();["q","offset","limit"].forEach(key=>{const value=params.get(key);if(value!==null)allowed.set(key,value)});if(!allowed.has("limit"))allowed.set("limit",String(config.pageSize))
  const query=useChillQuery<InventoryView>(`v1/inventory?${allowed}`),data=query.data,page=data?.classPage
  const [q,setQ]=React.useState(params.get("q")??""),[limit,setLimit]=React.useState(params.get("limit")??String(config.pageSize)),[open,setOpen]=React.useState(false)
  function navigate(offset:number) {
    const next=new URLSearchParams(params.toString());next.set("q",q);next.set("limit",limit);next.set("offset",String(offset));setOpen(false);router.replace(`/resources?${next}`)
  }
  const nodes=(data?.classes??[]).flatMap(row=>row.nodes),ready=nodes.filter(node=>node.ready==="True").length
  return <Screen back={back} title={!data?"장비":nodes.length?`노드 ${nodes.length}대 중 ${ready}대가 준비됐어요`:"등록된 노드가 없어요"} lede="등록된 장비 종류와 노드 상태예요. 실행 적합성이나 여유 용량을 보장하지는 않아요.">
    <Rows><RowDialog open={open} onOpenChange={setOpen} label="검색" hint={params.get("q")?`"${params.get("q")}"`:"전체 장비"} title="장비 검색">
      <form className="grid gap-4" onSubmit={event=>{event.preventDefault();navigate(0)}}>
        <label className="grid gap-1.5">이름<input value={q} onChange={event=>setQ(event.target.value)} maxLength={256}/></label>
        <label className="grid gap-1.5">한 번에 보기<input type="number" min="1" max={config.maxPageSize} value={limit} onChange={event=>setLimit(event.target.value)} required/></label>
        <Button type="submit">검색하기</Button>
      </form>
    </RowDialog></Rows>
    <QueryGate query={query}>{data&&<>
      {data.classes?.length?<Rows label="장비 종류">{data.classes.map(row=><RowDialog key={row.uid||row.name} label={row.name} hint={`${row.architecture} · ${row.memory}${row.accelerator?` · ${row.accelerator}`:""}`} value={`${row.nodes.filter(node=>node.ready==="True").length}/${row.nodes.length}대 준비`} title={row.name}>
        <ul className="grid gap-1.5">{row.nodes.map(node=><li key={node.uid} className="flex flex-wrap items-center gap-2"><span className="font-medium">{node.name}</span><StatusBadge tone={node.ready==="True"?"ok":node.ready==="False"?"warn":"idle"}>{node.ready==="True"?"준비됨":node.ready==="False"?"준비되지 않음":"상태 확인 안 됨"}</StatusBadge>{node.schedulingDisabled&&<StatusBadge tone="idle">배정 중지</StatusBadge>}</li>)}</ul>
        {!row.nodes.length&&<p>이 종류의 노드가 없어요.</p>}
        <Facts label="전력 모드" items={row.powerModes.map(mode=>[mode.name,powerModeText(mode.actuator)])}/>
        <p className="text-sm text-muted-foreground">등록된 사양과 노드 상태예요. 실행 적합성과 여유 용량을 보장하지 않아요.</p>
        <SourceJSON inline value={row}/>
      </RowDialog>)}</Rows>:<p>조건에 맞는 장비가 없어요.</p>}
      {page&&<div className="flex flex-wrap items-center gap-2"><span className="text-sm text-muted-foreground">총 {page.total}종류</span><Button variant="outline" size="sm" disabled={page.offset===0} onClick={()=>navigate(Math.max(0,page.offset-page.limit))}>이전</Button><Button variant="outline" size="sm" disabled={page.nextOffset===undefined} onClick={()=>navigate(page.nextOffset??0)}>다음</Button></div>}
      <Rows><RowDialog label="원본 자료와 내보내기" title="장비 조회 원본"><SourceJSON inline value={data}/><SourceExport value={{conditions:Object.fromEntries(allowed),source:data}} filename="chill-inventory.json"/></RowDialog></Rows>
    </>}</QueryGate>
  </Screen>
}

export function PoolsPage() {
  const query=useChillQuery<PoolsView>("v1/pools"),data=query.data
  return <Screen back={back} title={!data?"실행 그룹":data.items?.length?`실행 그룹이 ${data.items.length}개 있어요`:"실행 그룹이 없어요"} lede="노드를 묶어 서비스를 실행하는 단위예요.">
    <QueryGate query={query}>{data&&<>
      {!!data.items?.length&&<Rows label="실행 그룹">{data.items.map(row=><Row key={row.id} href={`/pools/${encodeURIComponent(row.id)}`} label={(row.currentNodes??[]).map(node=>node.name).join(", ")||"노드 없는 그룹"} hint={(row.servingWorkloads??[]).map(workload=>workload.name).join(", ")||"실행 중인 서비스 없음"} value={`인스턴스 ${row.instances?.length??0}개`}/>)}</Rows>}
      <Rows><RowDialog label="원본 자료" title="실행 그룹 조회 원본"><SourceJSON inline value={data}/></RowDialog></Rows>
    </>}</QueryGate>
    <Rows><Row href="/scheduler/candidates?view=snapshot" label="실제 자원 구성 보기" hint="운영 판단이 본 노드·인스턴스·할당"/></Rows>
  </Screen>
}

export function PoolPage({id}:{id:string}) {
  const [poll,setPoll]=React.useState(false)
  const query=useChillQuery<PoolState>(`v1/pools/${encodeURIComponent(id)}`,{poll}),data=query.data
  const instances=data?.instances??[],ready=instances.filter(row=>row.ready).length
  return <Screen back={["/pools","실행 그룹"]} title={!data?"실행 그룹":instances.length?`인스턴스 ${instances.length}개 중 ${ready}개가 준비됐어요`:"실행 중인 인스턴스가 없어요"} lede={data?`노드 ${(data.currentNodes??[]).map(node=>node.name).join(", ")||"없음"}`:undefined}>
    <QueryGate query={query}>{data&&<>
      {instances.length>0&&<Rows label="인스턴스">{instances.map(row=><RowDialog key={row.id} label={row.node.name} hint={(row.workloads??[]).map(workload=>workload.name).join(", ")||"서비스 없음"} value={<StatusBadge tone={row.ready?"ok":"warn"}>{row.ready?"준비됨":"준비되지 않음"}</StatusBadge>} title={`${row.node.name} 인스턴스`}>
        <Facts items={[["노드",row.node.name],["상태",row.ready?"준비됨":"준비되지 않음"],["서비스",(row.workloads??[]).map(workload=>workload.name).join(", ")||"없음"]]}/>
        <SourceJSON inline value={row}/>
      </RowDialog>)}</Rows>}
      <Rows>
        <RowDialog label="노드" value={`${data.currentNodes?.length??0}대`} title="그룹의 노드"><ul className="grid gap-1.5">{(data.currentNodes??[]).map(row=><li key={row.uid}>{row.name}</li>)}</ul>{!data.currentNodes?.length&&<p>노드가 없어요.</p>}</RowDialog>
        <RowDialog label="서비스" value={`${data.servingWorkloads?.length??0}개`} title="그룹에서 실행 중인 서비스"><ul className="grid gap-1.5">{(data.servingWorkloads??[]).map(row=><li key={row.uid}><Link href={`/workloads/${encodeURIComponent(row.name)}?uid=${encodeURIComponent(row.uid)}`}>{row.name}</Link></li>)}</ul>{!data.servingWorkloads?.length&&<p>실행 중인 서비스가 없어요.</p>}</RowDialog>
        <Row href={`/scheduler/candidates?view=snapshot&pool=${encodeURIComponent(id)}`} label="실제 자원 구성 보기" hint="자원 할당과 전력모드"/>
      </Rows>
    </>}</QueryGate>
    <Rows label="더 보기">
      <RowDialog label="자동 새로고침" value={poll?"켜짐":"꺼짐"} title="자동 새로고침"><label className="switch"><input type="checkbox" checked={poll} onChange={event=>setPoll(event.target.checked)}/>그룹 상태를 자동으로 확인하기</label></RowDialog>
      {data&&<RowDialog label="원본 자료와 내보내기" title="실행 그룹 원본"><p>현재 그룹 관측 {data.observedAt}</p><SourceJSON inline value={data}/><SourceExport value={data} filename={`chill-pool-${id}.json`}/></RowDialog>}
    </Rows>
  </Screen>
}
