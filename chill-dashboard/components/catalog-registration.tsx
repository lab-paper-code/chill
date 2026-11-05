"use client"

import { useState, type FormEvent, type ReactNode } from "react"
import { Facts, Row, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { catalogManifestKinds, parseCatalogManifest, type CatalogKind, type CatalogRegistration as Registration } from "@/lib/catalog-manifest"
import type { ResourceState } from "@/hooks/use-catalog-resource"

const fieldLabels: Record<string, string> = {
  artifacts: "파일", executionPaths: "실행 방식", source: "보관 위치", image: "보관 이미지 주소", architecture: "대상 장비 종류", files: "모델 파일 위치", path: "파일 위치", target: "대상", model: "모델", artifact: "모델 파일", name: "이름", format: "형식", input: "입력", output: "출력", fields: "필드", maxBytes: "최대 크기 · 바이트", runtime: "실행기", settings: "설정", tensors: "텐서", sampleShape: "한 요청의 크기", datatype: "값 종류",
}
function ProvidedValue({ value }: { value: unknown }) {
  if (Array.isArray(value)) return value.length ? <ol className="grid list-decimal gap-3 pl-5">{value.map((item, index) => <li key={index}><ProvidedValue value={item} /></li>)}</ol> : <span className="text-muted-foreground">빈 목록</span>
  if (value !== null && typeof value === "object") return Object.keys(value).length ? <dl className="grid gap-3 border-l pl-3">{Object.entries(value).map(([key, item]) => <div key={key} className="min-w-0"><dt className={["source", "runtime", "derivation"].includes(key) ? "sr-only" : "text-sm font-medium"}>{fieldLabels[key] ? `${fieldLabels[key]} (${key})` : key}</dt><dd className="mt-1 break-words text-sm [overflow-wrap:anywhere]">{["source", "runtime", "derivation"].includes(key) ? <details><summary className="min-h-11 cursor-pointer py-2 font-medium">{fieldLabels[key] ?? key} 상세</summary><ProvidedValue value={item} /></details> : <ProvidedValue value={item} />}</dd></div>)}</dl> : <span className="text-muted-foreground">빈 객체</span>
  return <span className="whitespace-pre-wrap">{value === null ? "null" : value === "" ? "빈 문자열" : String(value)}</span>
}

export function CatalogRegistration({ kind, label, r, problem, manual }: { kind: CatalogKind; label: string; r: ResourceState; problem: ReactNode; manual: () => void }) {
  const [importing, setImporting] = useState(false)
  const [text, setText] = useState("")
  const [reviewed, setReviewed] = useState<Registration>()
  const [validation, setValidation] = useState("")
  const [reading, setReading] = useState(false)
  const locked = r.blocked || r.conflict || reading
  function change(value: string) { setText(value); setReviewed(undefined); setValidation("") }
  function review(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (locked) return
    try { setReviewed(parseCatalogManifest(kind, text)); setValidation("") }
    catch (error) { setReviewed(undefined); setValidation((error as Error).message) }
  }
  if (!importing) return <Screen back={[`/${kind}`, label]} title={`${label} 등록`}>
    <p className="text-muted-foreground">담당자가 준비한 등록 문서를 가져오거나, 설정을 직접 작성하세요.</p>
    <Rows label="등록 방법">
      <Row label="준비된 YAML/JSON 가져오기" hint="파일을 선택하거나 붙여넣고, 내용을 검토한 뒤 등록해요." onClick={() => setImporting(true)} />
      <Row label="직접 작성하기" hint="이름부터 파일 위치와 설정까지 단계별로 입력해요." onClick={manual} />
    </Rows>
  </Screen>
  return <Screen back={[`/${kind}`, label]} title={`${label} 문서 가져오기`} status={<span className="text-sm text-muted-foreground">{reviewed ? "등록 전 검토" : "문서 입력"}</span>}
    action={<div className="flex flex-wrap gap-3"><Button type="button" variant="outline" disabled={locked} onClick={() => setImporting(false)}>등록 방법 바꾸기</Button>{reviewed && <Button type="button" disabled={locked} onClick={() => { if (!locked) void r.submit("POST", reviewed) }}>{r.mutation.isPending ? "등록하고 있어요" : `${label} 등록하기`}</Button>}</div>}>
    <p className="text-sm text-muted-foreground">{catalogManifestKinds[kind]} YAML/JSON 또는 CHILL API의 name·spec JSON을 읽어요. 파일 선택과 검토만으로는 등록되지 않아요.</p>
    <details open={!reviewed}><summary className="min-h-11 cursor-pointer py-2 font-medium">등록 문서 편집</summary><form onSubmit={review} aria-describedby={validation ? "manifest-error" : undefined} className="grid gap-4">
      <fieldset disabled={locked} className="grid min-w-0 gap-4">
        <label className="grid gap-2 text-sm">로컬 YAML/JSON 파일<Input type="file" accept=".yaml,.yml,.json,application/json,application/yaml,text/yaml" onChange={async event => {
          const file = event.target.files?.[0]
          if (!file) return
          setReading(true); setReviewed(undefined); setValidation("")
          try { change(await file.text()) }
          catch { setValidation("파일을 읽을 수 없어요. 다시 선택하거나 내용을 붙여넣어 주세요.") }
          finally { setReading(false) }
        }} /></label>
        {reading && <p role="status">파일을 읽고 있어요.</p>}
        <label className="grid gap-2 text-sm">YAML/JSON 문서<textarea className="min-h-64 w-full min-w-0 rounded-md border bg-background p-3 font-mono text-sm focus-visible:outline-2 focus-visible:outline-ring" required spellCheck={false} value={text} onChange={event => change(event.target.value)} /></label>
        <p className="text-sm text-muted-foreground">외부 주소에서 파일을 내려받거나 이미지를 만들지 않아요. 이름과 spec만 API에 전달하며, 원본의 식별자·상태 정보는 보내지 않아요.</p>
        <Button type="submit" variant="outline" className="w-fit">문서 검토하기</Button>
      </fieldset>
      {validation && <p id="manifest-error" role="alert" className="notice">{validation}</p>}
    </form></details>
    {reviewed && <section aria-label="등록 내용 검토" className="grid min-w-0 gap-5">
      <Facts items={[["등록 이름", reviewed.name], ["종류", `${label} (${catalogManifestKinds[kind]})`]]} />
      <div className="grid gap-3"><h2 className="font-medium">제공된 설정</h2><ProvidedValue value={reviewed.spec} /></div>
      <p className="text-sm text-muted-foreground">설정은 입력한 그대로 전달해요. 사용 가능한 설정과 연결 관계는 등록할 때 서버가 확인해요. 수정하려면 위 문서를 고친 뒤 다시 검토하세요.</p>
    </section>}
    {problem}
  </Screen>
}
