"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useState } from "react"
import { useChillQuery } from "@/lib/client"
import { QueryGate, SourceJSON, SourceExport } from "@/components/read-region"
import { Empty, Facts, Row, RowDialog, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { resourceHref as href, type ModelResource, type PreprocessorResource, type WorkloadSummary } from "@/lib/domain"
import { explain } from "@/lib/labels"
import type { PoolsView } from "@/lib/read-types"
import { ServiceAdmission } from "@/features/service-hub"
import { DataTable } from "@/components/data-table"
import { SettingsNavigation } from "@/components/settings-navigation"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { connected, unwrap, useResource, type Catalog, type Referenced } from "@/hooks/use-catalog-resource"
import { Deleted, DeleteConfirm, DeleteRow, DefinitionSummary, Impact, Mismatch, Problem, fieldList, kindLabel, usersOf } from "@/features/catalog-editor"

export function ModelPage({ name,expectedUID,embedded=false }: { name: string;expectedUID?:string;embedded?:boolean }) {
  const r = useResource("models", name,expectedUID)
  const model = r.detail.data && !("desired" in r.detail.data) && !r.denied ? r.detail.data as ModelResource : undefined
  const [deleting, setDeleting] = useState(false)
  const preprocessors = useChillQuery<{ items: PreprocessorResource[] }>("v1/preprocessors")
  if (r.deletedUID) return <Deleted kind="models" name={name} uid={r.deletedUID} r={r} />
  if ((r.wrongUID || r.identityChanged) && r.current) return <Mismatch kind="models" name={name} uid={r.current.metadata.uid} />
  const uid = model?.metadata.uid ?? ""
  const count = r.affected?.length
  return <Screen embedded={embedded} back={embedded?undefined:["/settings?tab=models", "모델"]} title={name}>
    <QueryGate query={r.detail}>{model && <>
      <Problem r={r} kind="models" />
      <section aria-label="현재 정의"><h2 className="font-semibold">현재 정의</h2><DefinitionSummary spec={model.spec}/></section>
      <section aria-label="연결 대상·영향"><h2 className="font-semibold">연결 대상·영향</h2><Facts items={[["연결 서비스",count===undefined?r.workloads.error?"조회 실패":"조회 중":`${count}개`]]}/><Impact kind="models" affected={r.affected}/></section>
      <Rows label="관리">
        <Row href={href("models", name, uid, "edit")} label="설정 수정" hint="파일과 실행 방식 바꾸기" />
        <Row href={href("models",name,uid,"files")} label="파일·실행 방식 상세"/>
        <DeleteRow kind="models" r={r} users={usersOf(r.affected, preprocessors.data?.items.filter(p => p.spec.artifacts.some(a => a.target.model === name)))} open={() => setDeleting(true)} />
        <RowDialog label="원본 자료 보기" title="모델 전체 원본"><SourceJSON inline value={model} /><SourceExport value={model} filename={`model-${name}.json`} /></RowDialog>
      </Rows>
      {deleting && <DeleteConfirm kind="models" name={name} r={r} close={() => setDeleting(false)} />}
    </>}</QueryGate>
  </Screen>
}

export function ModelSectionPage({ name, section }: { name: string; section: "files" | "services" }) {
  const r = useResource("models", name)
  const preprocessors = useChillQuery<{ items: PreprocessorResource[] }>("v1/preprocessors", { enabled: section === "files" })
  const pools = useChillQuery<PoolsView>("v1/pools", { enabled: section === "services" })
  const model = r.detail.data && !("desired" in r.detail.data) && !r.denied ? r.detail.data as ModelResource : undefined
  if ((r.wrongUID || r.identityChanged) && r.current) return <Mismatch kind="models" name={name} uid={r.current.metadata.uid} />
  const back: [string, string] = [href("models", name, model?.metadata.uid ?? r.expectedUID ?? ""), name]
  if (section === "services") {
    const affected = r.affected
    return <Screen back={back} title="연결 서비스">
      <QueryGate query={r.detail}><QueryGate query={r.workloads}>{affected?.length ? <DataTable searchable={false} data={affected} columns={[
        {key:"service",label:"서비스",value:view=><Link href={href("workloads",view.desired.metadata.name,view.desired.metadata.uid)}>{view.desired.metadata.name}</Link>},
        {key:"devices",label:"장치",value:view=>{
          if(pools.error)return "조회 실패"
          if(!pools.data)return "조회 중"
          const pool=(pools.data.workloads??[]).find(pool=>pool.workload.uid===view.desired.metadata.uid)
          return !pool?.nodes?"배치 미확인":pool.nodes.map(node=>node.name).join(", ")||"배치 없음"
        }},
        {key:"admission",label:"접수 상태",value:view=><ServiceAdmission view={view}/>},
      ]}/> : <Empty title="이 모델을 쓰는 서비스가 없어요" />}</QueryGate></QueryGate>
    </Screen>
  }
  const spec = model?.spec
  return <Screen back={back} title="파일·실행 방식">
    <QueryGate query={r.detail}>{spec && <>
      <Rows label="모델 파일">{spec.artifacts.map((a, i) => {
        const targeting = preprocessors.data?.items.filter(p => p.spec.artifacts.some(t => t.target.model === name && t.target.artifact === a.name))
        return <RowDialog key={i} label={`${a.format.toUpperCase()} 모델 파일 ${i+1}`} hint={a.source.architecture} value={`모델 파일 ${a.source.files.length}개`} title={`${a.format.toUpperCase()} 모델 파일 ${i+1}`}>
        <Facts items={[
          ["형식", a.format.toUpperCase()], ["대상 장비", a.source.architecture],
          ...(a.input?.tensors ?? []).map((t): [string, string] => [`RKNN 입력 ${t.name}`, `${t.datatype} · 크기 ${t.sampleShape.join(" × ")}`]),
          ["전처리기", targeting === undefined ? preprocessors.error ? "확인 불가" : "확인 중" : targeting.map(p => p.metadata.name).join(", ") || "이 파일을 대상으로 하는 전처리기가 없어요"],
          ["변환", a.derivation ? `${a.derivation.conversion.tool} ${a.derivation.conversion.version} · ${a.derivation.targetPlatform}` : "원본 파일"],
        ]} />
        <details><summary className="min-h-11 cursor-pointer py-2">파일 식별자·보관 위치 원문</summary><SourceJSON inline value={a}/></details>
      </RowDialog>})}</Rows>
      <Rows label="실행 방식">{spec.executionPaths.map((p, i) => {
        const ff = p.runtime.settings?.firstfloor
        const artifactIndex=spec.artifacts.findIndex(a=>a.name===p.artifact)
        const title=`실행 방식 ${i+1} · ${explain(p.runtime.backend)[0]}`
        return <RowDialog key={i} label={title} hint={artifactIndex<0?"연결 파일 미확인":`모델 파일 ${artifactIndex+1} 사용`} title={title}>
          <Facts items={[["사용하는 파일", artifactIndex<0?"연결 파일 미확인":`모델 파일 ${artifactIndex+1}`],["처리 방식",explain(p.runtime.backend)[0]]]}/>
          {ff?<table aria-label="실행 한도" className="w-full text-left text-sm tabular-nums"><tbody><tr><th scope="row">최대 묶음 크기 (건)</th><td className="text-right">{ff.maxBatchSize}</td></tr><tr><th scope="row">서비스별 대기 한도 (건)</th><td className="text-right">{ff.maxQueueSize}</td></tr><tr><th scope="row">응답 목표 초과 정책</th><td>{ff.deadlinePolicy==="drop"?"실행 전 거절":ff.deadlinePolicy==="defer"?"뒤로 미루기":"계속 처리"}</td></tr></tbody></table>:<p>요청 처리 설정 사용 안 함</p>}
          <details><summary className="min-h-11 cursor-pointer py-2">실행 방식 식별자·설정 원문</summary><SourceJSON inline value={p}/></details>
        </RowDialog>
      })}</Rows>
      <Rows><RowDialog label="원본 자료 보기" title="파일과 실행 설정 원본"><SourceJSON inline value={spec} /></RowDialog></Rows>
    </>}</QueryGate>
  </Screen>
}

export function CatalogSettingsPage({kind}:{kind:Referenced}) {
  const list=useChillQuery<{items:(ModelResource|Catalog)[]}>(`v1/${kind}`)
  const workloads=useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const params=useSearchParams(),router=useRouter()
  const selected=params.get("name")?{name:params.get("name")!,uid:params.get("uid")??""}:null
  const label=kind==="preprocessors"?"입력 처리":kindLabel(kind)
  return <Screen title="설정" width="data" actionPlacement="header" action={<Button asChild><Link href={`/${kind}/new`}>{label} 등록</Link></Button>}>
    <SettingsNavigation active={kind}/>
    <section aria-label={`${label} 목록`}><QueryGate query={list}><DataTable urlState data={list.data?.items??[]} columns={[
      {key:"name",label:"이름",sortValue:item=>item.metadata.name,value:item=><Link className="underline" href={`/settings?${new URLSearchParams({...Object.fromEntries(params),tab:kind,name:item.metadata.name,uid:item.metadata.uid})}`} scroll={false}>{item.metadata.name}</Link>},
      {key:"definition",label:"정의",value:item=>{
        const spec=item.spec
        const rows:[string,string][]="input" in spec?[["입력",fieldList(spec.input.fields)],["출력",fieldList(spec.output.fields)],["대상 모델",[...new Set(spec.artifacts.map(a=>a.target.model))].join(", ")||"없음"]]
          :"source" in spec?[["기록 필드",fieldList(spec.fields)]]
          :[["파일 형식",[...new Set(spec.artifacts.map(a=>a.format))].join(", ")||"없음"],["실행 유형",[...new Set(spec.executionPaths.map(p=>explain(p.runtime.backend)[0]))].join(", ")||"없음"]]
        return rows.map(([name,value])=><div key={name}><span className="text-muted-foreground">{name}: </span>{value}</div>)
      }},
      {key:"count",label:kind==="models"?"모델 파일":kind==="datasets"?"기록 필드":"대상 모델",unit:"개",numeric:true,value:item=>"input" in item.spec?new Set(item.spec.artifacts.map(a=>a.target.model)).size:"source" in item.spec?item.spec.fields.length:item.spec.artifacts.length},
      ...(kind==="models"?[{key:"paths",label:"실행 방식",unit:"개",numeric:true,value:(item:ModelResource|Catalog)=>"executionPaths" in item.spec?item.spec.executionPaths.length:0}]:[]),
      {key:"services",label:"연결 서비스",unit:"개",numeric:true,value:item=>workloads.error?"조회 실패":workloads.data?connected(workloads.data,kind,item.metadata.name,item.metadata.uid)?.length:"조회 중"},
      {key:"actions",label:"작업",value:item=><Link className="underline" href={href(kind,item.metadata.name,item.metadata.uid,"edit")}>수정</Link>},
      {key:"source",label:"원문",value:item=><details><summary className="min-h-11 cursor-pointer py-2">정의 원문</summary><SourceJSON inline value={item.spec}/></details>},
    ]}/></QueryGate></section>
    <details><summary className="min-h-11 cursor-pointer py-2">목록 원본</summary><SourceJSON inline value={list.data}/></details>
    <Sheet open={!!selected} onOpenChange={open=>{if(!open){const next=new URLSearchParams(params.toString());next.delete("name");next.delete("uid");router.replace(`/settings?${next}`,{scroll:false})}}}><SheetContent className="w-full overflow-y-auto sm:max-w-3xl" aria-describedby={undefined}><SheetHeader><SheetTitle>{selected?.name??label}</SheetTitle></SheetHeader>{selected&&<div className="px-4 pb-6">{kind==="models"?<ModelPage key={selected.uid} name={selected.name} expectedUID={selected.uid} embedded/>:<CatalogPage key={selected.uid} kind={kind} name={selected.name} expectedUID={selected.uid} embedded/>}</div>}</SheetContent></Sheet>
  </Screen>
}

export function CatalogPage({ kind, name,expectedUID,embedded=false }: { kind: "preprocessors" | "datasets"; name: string;expectedUID?:string;embedded?:boolean }) {
  const r = useResource(kind, name,expectedUID)
  const item = r.detail.data && !r.denied ? unwrap(r.detail.data) as Catalog : undefined
  const [deleting, setDeleting] = useState(false)
  if (r.deletedUID) return <Deleted kind={kind} name={name} uid={r.deletedUID} r={r} />
  if ((r.wrongUID || r.identityChanged) && r.current) return <Mismatch kind={kind} name={name} uid={r.current.metadata.uid} />
  const uid = item?.metadata.uid ?? ""
  const count = r.affected?.length
  const spec = item?.spec
  return <Screen embedded={embedded} back={embedded?undefined:[`/settings?tab=${kind}`, kindLabel(kind)]} title={name}>
    <QueryGate query={r.detail}>{item && spec && <>
      <Problem r={r} kind={kind} />
      <Facts items={[["연결 서비스",count===undefined?r.workloads.error?"조회 실패":"조회 중":`${count}개`]]}/>
      <section aria-label="현재 정의"><h2 className="font-semibold">현재 정의</h2><DefinitionSummary spec={spec}/></section>
      <section aria-label="연결 대상·영향"><h2 className="font-semibold">연결 대상·영향</h2><Impact kind={kind} affected={r.affected}/></section>
      <Rows label="관리">
        <Row href={href(kind, name, uid, "edit")} label="설정 수정" hint={kind === "datasets" ? "데이터 위치와 기록 필드 바꾸기" : "필드와 대상 모델 파일 바꾸기"} />
        <DeleteRow kind={kind} r={r} users={usersOf(r.affected)} open={() => setDeleting(true)} />
        <RowDialog label="원본 자료 보기" title={`${kindLabel(kind)} 전체 원본`}><SourceJSON inline value={item} /><SourceExport value={item} filename={`${kind.slice(0, -1)}-${name}.json`} /></RowDialog>
      </Rows>
      {deleting && <DeleteConfirm kind={kind} name={name} r={r} close={() => setDeleting(false)} />}
    </>}</QueryGate>
  </Screen>
}

