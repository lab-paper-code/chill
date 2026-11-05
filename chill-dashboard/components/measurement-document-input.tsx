"use client"

import { useId, useState, type FormEvent } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { parseMeasurementDocument, type MeasurementRequestDocument } from "@/lib/measurement-document"

export function MeasurementDocumentInput({ namespace, locked, reviewed, onReview, onEdit, example }: {
  namespace: string
  locked: boolean
  reviewed: boolean
  onReview: (body: MeasurementRequestDocument) => void
  onEdit: () => void
  example: string
}) {
  const id = useId()
  const [text, setText] = useState("")
  const [validation, setValidation] = useState("")
  const [reading, setReading] = useState(false)
  const disabled = locked || reading
  function change(value: string) {
    if (disabled) return
    setText(value)
    setValidation("")
    onEdit()
  }
  function review(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (disabled) return
    try {
      const body = parseMeasurementDocument(text, namespace)
      setValidation("")
      onReview(body)
    } catch (error) {
      onEdit()
      setValidation((error as Error).message)
    }
  }
  return <details open={!reviewed}>
    <summary className="min-h-11 cursor-pointer py-2 font-medium">측정 문서 편집</summary>
    <form onSubmit={review} aria-describedby={validation ? `${id}-error` : undefined} className="grid min-w-0 gap-4">
      <fieldset disabled={disabled} className="grid min-w-0 gap-4">
        <legend className="sr-only">측정 요청 문서 입력</legend>
        <p id={`${id}-help`} className="max-w-prose text-sm text-muted-foreground">BatchEnergyOptimization YAML/JSON 파일을 선택하거나 붙여넣으세요. 파일 선택과 문서 검토는 서버에 요청을 보내지 않아요. 비밀번호나 토큰을 넣지 마세요.</p>
        <label className="grid gap-2 text-sm">로컬 YAML/JSON 파일<Input type="file" accept=".yaml,.yml,.json,application/json,application/yaml,text/yaml" aria-describedby={`${id}-help`} onChange={async event => {
          if (disabled) return
          const file = event.target.files?.[0]
          if (!file) return
          onEdit()
          setReading(true)
          setValidation("")
          try { setText(await file.text()) }
          catch { setValidation("파일을 읽을 수 없어요. 다시 선택하거나 내용을 붙여넣어 주세요.") }
          finally { setReading(false) }
        }} /></label>
        {reading && <p role="status">파일을 읽고 있어요.</p>}
        <label className="grid gap-2 text-sm">YAML/JSON 문서<textarea className="min-h-64 w-full min-w-0 rounded-md border bg-background p-3 font-mono text-sm focus-visible:outline-2 focus-visible:outline-ring" required spellCheck={false} value={text} aria-invalid={validation ? true : undefined} aria-describedby={`${id}-help${validation ? ` ${id}-error` : ""}`} onChange={event => change(event.target.value)} /></label>
        <div className="grid gap-2">
          <Button type="button" variant="outline" className="w-fit" disabled={disabled || text.length > 0} aria-describedby={`${id}-example-help`} onClick={() => { if (!disabled && text.length === 0) change(example) }}>예시를 입력란에 넣기</Button>
          <p id={`${id}-example-help`} className="text-sm text-muted-foreground">{text.length > 0 ? "작성 중인 문서를 보존하고 있어요. 예시를 넣으려면 문서를 따로 저장한 뒤 입력란을 비워 주세요." : "빈 입력란에만 구조 예시를 넣어요. 실제 값으로 고친 뒤 검토하세요."}</p>
        </div>
        <p className="max-w-prose text-sm text-muted-foreground">네임스페이스가 없으면 선택한 {namespace || "네임스페이스"}에 보내요. 상태와 서버 식별 정보는 제외하며, 제공된 spec과 labels·annotations는 유지해요. 설정과 참조의 유효성은 이후 서버 확인에서 검사해요.</p>
        <Button type="submit" variant="outline" className="w-fit">문서 검토하기</Button>
      </fieldset>
      {validation && <p id={`${id}-error`} role="alert" className="notice">{validation}</p>}
    </form>
  </details>
}
