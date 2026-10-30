"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { useChillQuery, useSession } from "@/lib/client"
import type { InstanceState, InventoryView, PoolNode, PoolsView, PowerModeActuator } from "@/lib/read-types"
import { explain } from "@/lib/labels"
import { QueryGate, SourceJSON, SourceExport, StatusBadge } from "@/components/read-region"
import { Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"

const back:[string,string]=["/scheduler","운영 판단"]

// api/v1alpha1/deviceclass_types.go: how ChillServer applies one declared power mode. Shared with the measurement request.
export function powerModeText(actuator:PowerModeActuator) {
  switch(actuator.kind){
    case "Fixed": return "장비 기본 상태 그대로 써요"
    case "JetsonNVPModel": return `Jetson 전력 모드 ${actuator.jetsonNVPModel.id}번으로 바꿔요`
    case "NvidiaLockedGPUClock": return `GPU 클럭을 ${actuator.nvidiaLockedGPUClock.minMHz}~${actuator.nvidiaLockedGPUClock.maxMHz} MHz로 고정해요`
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

// One plain sentence per reason code; the code itself stays in the 원본 view.
function NodeRows({label,rows}:{label:string;rows:PoolNode[]}) {
  return <Rows label={label}>{rows.map(row=><Row key={row.node.uid} label={row.node.name} hint={[row.powerMode&&`전력모드 ${row.powerMode}`,...(row.reasons??[]).map(reason=>explain(reason)[0])].filter(Boolean).join(" ")||undefined} value={<StatusBadge tone={row.ready?"ok":"warn"}>{row.ready?"준비됨":"준비 중"}</StatusBadge>}/>)}</Rows>
}
function InstanceRows({label,rows}:{label:string;rows:InstanceState[]}) {
  return <Rows label={label}>{rows.map(row=><RowDialog key={row.id} label={row.node.name} hint={(row.workloads??[]).map(workload=>workload.name).join(", ")||"연결된 서비스 없음"} value={<StatusBadge tone={row.ready?"ok":"warn"}>{row.reason==="PolicyAckPending"?"반영 대기":row.ready?"준비됨":"준비 중"}</StatusBadge>} title={`${row.node.name} 인스턴스`}>
    <Facts items={[["인스턴스",row.id],["노드",row.node.name],["상태",row.reason?explain(row.reason)[0]:row.ready?"준비됨":"준비 중"],["서비스",(row.workloads??[]).map(workload=>workload.name).join(", ")||"없음"]]}/>
    <SourceJSON inline value={row}/>
  </RowDialog>)}</Rows>
}

// Every Node once per role: each Workload's active POOL, the measurement POOL, available Nodes, and the rest.
export function PoolsPage() {
  const query=useChillQuery<PoolsView>("v1/pools"),data=query.data,pools=data?.workloads??[]
  const shared=new Set(pools.flatMap(pool=>pool.nodes??[]).map(node=>node.uid).filter((uid,index,all)=>all.indexOf(uid)!==index))
  return <Screen back={back} title={!data?"실행 그룹":pools.length?`서비스 ${pools.length}개가 실행 그룹을 쓰고 있어요`:"실행 중인 서비스가 없어요"} lede="서비스마다 인스턴스가 있는 노드가 그 서비스의 실행 그룹이에요. 여러 서비스가 한 노드를 함께 쓸 수 있어요.">
    <QueryGate query={query}>{data&&<>
      {!!pools.length&&<Rows label="서비스별 실행 그룹">{pools.map(pool=><Row key={pool.workload.uid} href={`/pools/${encodeURIComponent(pool.workload.uid)}`} label={pool.workload.name} hint={(pool.nodes??[]).map(node=>shared.has(node.uid)?`${node.name}(함께 씀)`:node.name).join(", ")||"노드 없음"} value={`인스턴스 ${pool.instances?.length??0}개`}/>)}</Rows>}
      {!!data.measurement?.length&&<Rows label="측정 중인 노드">{data.measurement.map(row=><RowDialog key={row.node.uid} label={row.node.name} value={`인스턴스 ${row.instances?.length??0}개`} title={`${row.node.name}에서 측정 중인 인스턴스`}>
        {row.instances?.length?<InstanceRows label="인스턴스" rows={row.instances}/>:<p>이 노드에는 CHILL 인스턴스가 없어요.</p>}
        <SourceJSON inline value={row}/>
      </RowDialog>)}</Rows>}
      {!!data.available?.length&&<NodeRows label="바로 쓸 수 있는 노드" rows={data.available}/>}
      {!!data.other?.length&&<NodeRows label="그 밖의 노드" rows={data.other}/>}
      <Rows><RowDialog label="원본 자료" title="실행 그룹 조회 원본"><p>관측 {data.observedAt}</p><SourceJSON inline value={data}/></RowDialog></Rows>
    </>}</QueryGate>
    <Rows><Row href="/scheduler/candidates?view=snapshot" label="실제 자원 구성 보기" hint="운영 판단이 본 노드와 인스턴스, 할당"/></Rows>
  </Screen>
}

