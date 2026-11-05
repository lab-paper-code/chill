"use client"

import { useState, type FormEvent, type ReactNode } from "react"
import { Facts, Row, Rows, Screen } from "@/components/screen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { catalogManifestKinds, parseCatalogManifest, type CatalogKind, type CatalogRegistration as Registration } from "@/lib/catalog-manifest"
import { carrierBuildExample, carrierDockerfile, catalogGuidance, catalogPreparationSources, preprocessorContract } from "@/lib/catalog-guidance"
import type { ResourceState } from "@/hooks/use-catalog-resource"

const fieldLabels: Record<string, string> = {
  artifacts: "파일", executionPaths: "실행 방식", source: "보관 위치", image: "보관 이미지 주소", architecture: "대상 장비 종류", files: "모델 파일 위치", path: "파일 위치", target: "대상", model: "모델", artifact: "모델 파일", name: "이름", format: "형식", input: "입력", output: "출력", fields: "필드", maxBytes: "최대 크기 · 바이트", runtime: "실행기", settings: "설정", tensors: "텐서", sampleShape: "한 요청의 크기", datatype: "값 종류",
}
function ProvidedValue({ value }: { value: unknown }) {
  if (Array.isArray(value)) return value.length ? <ol className="grid list-decimal gap-3 pl-5">{value.map((item, index) => <li key={index}><ProvidedValue value={item} /></li>)}</ol> : <span className="text-muted-foreground">빈 목록</span>
  if (value !== null && typeof value === "object") return Object.keys(value).length ? <dl className="grid gap-3 border-l pl-3">{Object.entries(value).map(([key, item]) => <div key={key} className="min-w-0"><dt className={["source", "runtime", "derivation"].includes(key) ? "sr-only" : "text-sm font-medium"}>{fieldLabels[key] ? `${fieldLabels[key]} (${key})` : key}</dt><dd className="mt-1 break-words text-sm [overflow-wrap:anywhere]">{["source", "runtime", "derivation"].includes(key) ? <details><summary className="min-h-11 cursor-pointer py-2 font-medium">{fieldLabels[key] ?? key} 상세</summary><ProvidedValue value={item} /></details> : <ProvidedValue value={item} />}</dd></div>)}</dl> : <span className="text-muted-foreground">빈 객체</span>
  return <span className="whitespace-pre-wrap">{value === null ? "null" : value === "" ? "빈 문자열" : String(value)}</span>
}

function PreparationGuide({ kind, insert, hasDraft = false, locked = false }: { kind: CatalogKind; insert?: () => void; hasDraft?: boolean; locked?: boolean }) {
  const guide = catalogGuidance[kind]
  return <section aria-label="등록 준비 안내" className="grid min-w-0 gap-3 text-sm">
    <h2 className="font-medium">등록 전에 준비하세요</h2>
    <p className="max-w-prose text-muted-foreground">{guide.preparation}</p>
    <p>등록 순서: 모델 → 처리 모듈 + 데이터셋 → 서비스 → 측정</p>
    <details>
      <summary className="min-h-11 cursor-pointer py-2 font-medium">준비 책임과 이미지 제작 방법</summary>
      <div className="grid min-w-0 gap-4 pt-2">
        <ul className="grid list-disc gap-2 pl-5">
          <li>외부 제작자: 사용 권한이 있는 원본을 확보하고, 변환·모듈 작성·표본 구성을 마친 뒤 OCI 보관 이미지를 제작해 레지스트리에 push해요.</li>
          <li>플랫폼 관리자: CHILL API 등록 권한, 설치된 실행 방식·장비 아키텍처, 사설 이미지 pull 권한과 인증을 준비해요. 비밀번호나 토큰은 등록 문서에 넣지 마세요.</li>
          <li>운영자: 아래 문서를 실제 값으로 완성해 검토하고 CHILL에 등록해요. 등록은 선언을 저장할 뿐이며, 이미지 접근·실행 호환성·준비 상태와 성능은 이후 준비·측정에서 확인해요.</li>
        </ul>
        <p><code>source.image</code>는 로컬 파일이나 웹 다운로드 주소가 아니라 push한 OCI 이미지의 불변 digest 주소예요. <code>source.files</code>는 모델 파일들의 이미지 내부 절대 경로이고, <code>source.path</code>는 처리 모듈·데이터셋 디렉터리의 이미지 내부 절대 경로예요.</p>
        <p>보관 이미지는 파일을 복사하는 데 쓰이며 추론 서버 이미지가 아니에요. <code>sh</code>와 <code>cp</code>가 있어야 해요. 모델·처리 모듈의 <code>architecture</code>는 이미지와 대상 장비의 Kubernetes 아키텍처(<code>amd64</code> 또는 <code>arm64</code>)와 맞춰요. 데이터셋 이미지는 두 아키텍처를 지원하는 이미지 index로 준비해요.</p>
        <div className="grid min-w-0 gap-2"><h3 className="font-medium">이미지 안에 필요한 파일</h3><pre className="overflow-x-auto rounded-md border p-3 text-xs"><code>{guide.layout}</code></pre></div>
        {kind === "models" && <p>관리자에게 설치된 Profiler 실행 선언의 CPU 경로 이름과 설정을 받거나, 같은 실행 방식의 기존 등록 모델을 참고하세요. 예시의 <code>maxBatchSize</code>와 <code>maxQueueSize</code>는 권장값이 아니에요. 실제 지원 배치·큐 제한에 맞는 양의 정수로 따옴표까지 바꾸세요.</p>}
        {kind === "preprocessors" && <div className="grid min-w-0 gap-3"><p><code>preprocessor.py</code>는 아래 계약을 실제 모델에 맞게 구현해야 해요. 입력은 필드별 bytes, 출력은 성공 요청마다 UTF8 label bytes예요.</p><pre className="overflow-x-auto rounded-md border p-3 text-xs"><code>{preprocessorContract}</code></pre><p><code>preprocess</code>의 두 번째 반환값은 요청 순서대로 성공 단위 수(1 이상 정수) 또는 실패 사유 문자열이에요. 모델 입력에는 성공 요청만 넣고, <code>postprocess</code>는 성공 수만큼 출력해요. <code>sessions</code>의 키는 모델 파일 확장자를 뺀 이름이에요. 토크나이저·설정 파일도 같은 디렉터리에 넣고, Python 의존성은 관리자가 설치한 firstfloor 실행 이미지에 있는지 확인하세요. 보관 이미지에 라이브러리를 넣어도 실행 환경에 설치되지는 않아요.</p></div>}
        {kind === "datasets" && <p>레코드 ID는 각 표본 디렉터리 이름이에요. 이 예시는 <code>image</code> 파일을 JPEG 원본 바이트로 저장하며 확장자를 붙이지 않아요. <code>fields</code>의 이름·형식·최대 바이트 수를 실제 파일과 처리 모듈 입력에 맞추세요. 여러 입력 필드가 있으면 각 레코드에 필드별 파일을 추가해요.</p>}
        <div className="grid min-w-0 gap-2"><h3 className="font-medium">제작자용 보관 이미지 예시</h3><p>실제 파일을 로컬 <code>artifact/</code>에 위 구조대로 모으세요. 실행 가능한 베이스 이미지와 레지스트리 push 권한, Docker Buildx 환경이 필요해요. 다음 내용을 <code>Dockerfile</code>로 저장하세요.</p><pre className="overflow-x-auto rounded-md border p-3 text-xs"><code>{carrierDockerfile}</code></pre><p>그다음 외부 터미널에서 제작·게시하고 확인하세요. 이 화면에서는 실행하지 않아요.</p><pre className="overflow-x-auto rounded-md border p-3 text-xs"><code>{carrierBuildExample}</code></pre><p>push 후 선언한 각 아키텍처에서 이미지를 실행해 <code>sh</code>·<code>cp</code>와 모든 경로의 파일 읽기를 확인하세요. CHILL은 이 제작·업로드 작업을 대신하지 않아요.</p></div>
        <details><summary className="min-h-11 cursor-pointer py-2 font-medium">외부 공식 준비 문서</summary><div className="grid gap-3 pt-2"><p>업로드·패키징·인증 책임을 참고하는 고정 버전 문서예요. CHILL이 Hugging Face·S3·HTTP URI를 직접 가져오거나 Bento·MLflow 모델을 자동 변환한다는 뜻은 아니에요.</p><ul className="grid gap-2">{catalogPreparationSources.map(source => <li key={source.href}><a className="inline-flex min-h-11 items-center text-primary underline underline-offset-4" href={source.href} target="_blank" rel="noopener noreferrer">{source.label} (새 창)</a></li>)}</ul></div></details>
      </div>
    </details>
    <details>
      <summary className="min-h-11 cursor-pointer py-2 font-medium">{catalogManifestKinds[kind]} YAML 예시</summary>
      <div className="grid min-w-0 gap-3 pt-2">
        <p>바로 쓸 수 있는 자산이 아닌 구조 예시예요. 모든 <code>{"<...>"}</code>를 바꾸고 실제 파일·이미지·경로·설정을 준비하세요. 예시 삽입만으로 등록하거나 준비 완료로 처리하지 않아요.</p>
        <pre className="max-h-96 overflow-auto rounded-md border p-3 text-xs"><code>{guide.example}</code></pre>
        {insert ? <><Button type="button" variant="outline" className="w-fit" disabled={locked || hasDraft} aria-describedby="catalog-example-help" onClick={insert}>예시를 입력란에 넣기</Button><p id="catalog-example-help" className="text-muted-foreground">{hasDraft ? "작성 중인 문서를 보존하고 있어요. 예시를 넣으려면 먼저 문서를 따로 저장하고 입력란을 비워 주세요." : "입력란에만 넣어요. 실제 값으로 고친 뒤 문서 검토와 등록을 따로 진행하세요."}</p></> : <p className="text-muted-foreground">위의 YAML/JSON 가져오기를 선택하면 빈 입력란에 이 예시를 넣을 수 있어요.</p>}
      </div>
    </details>
  </section>
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
    <PreparationGuide kind={kind} />
  </Screen>
  return <Screen back={[`/${kind}`, label]} title={`${label} 문서 가져오기`} status={<span className="text-sm text-muted-foreground">{reviewed ? "등록 전 검토" : "문서 입력"}</span>}
    action={<div className="flex flex-wrap gap-3"><Button type="button" variant="outline" disabled={locked} onClick={() => setImporting(false)}>등록 방법 바꾸기</Button>{reviewed && <Button type="button" disabled={locked} onClick={() => { if (!locked) void r.submit("POST", reviewed) }}>{r.mutation.isPending ? "등록하고 있어요" : `${label} 등록하기`}</Button>}</div>}>
    <p className="text-sm text-muted-foreground">{catalogManifestKinds[kind]} YAML/JSON 또는 CHILL API의 name·spec JSON을 읽어요. 파일 선택과 검토만으로는 등록되지 않아요.</p>
    <PreparationGuide kind={kind} hasDraft={text.length > 0} locked={locked} insert={() => { if (!locked && text.length === 0) change(catalogGuidance[kind].example) }} />
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
