"use client"

import { useRef, useState } from "react"
import { Button, Fields, FormSection } from "@/components/kit"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { parseCatalogManifest, type CatalogRegistration } from "@/lib/catalog-manifest"
import { summarize, type Kind } from "@/lib/catalog-editor"

/** Paste or load a YAML/JSON registration document, review its summary, register it. */
export function ImportTab({ kind, text, onText, locked, pending, onRegister }: {
  kind: Kind; text: string; onText: (text: string) => void; locked: boolean; pending: boolean; onRegister: (registration: CatalogRegistration) => void
}) {
  const [reviewed, setReviewed] = useState<CatalogRegistration>()
  const [error, setError] = useState("")
  const file = useRef<HTMLInputElement>(null)
  const edit = (value: string) => { onText(value); setReviewed(undefined); setError("") }
  function review() {
    try { setReviewed(parseCatalogManifest(kind, text)); setError("") }
    catch (e) { setReviewed(undefined); setError((e as Error).message) }
  }
  return (
    <div className="grid gap-4">
      <FormSection title="문서">
        <div>
          <input ref={file} type="file" accept=".yaml,.yml,.json" hidden aria-label="문서 파일" onChange={event => { void event.target.files?.[0]?.text().then(edit); event.target.value = "" }} />
          <Button type="button" variant="outline" disabled={locked} onClick={() => file.current?.click()}>파일 선택</Button>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="catalog-document">YAML 또는 JSON</Label>
          <Textarea id="catalog-document" value={text} onChange={event => edit(event.target.value)} rows={12} spellCheck={false} className="min-h-56 font-mono text-xs" aria-invalid={!!error} aria-describedby={error ? "catalog-document-error" : undefined} />
          {error && <p id="catalog-document-error" role="alert" className="text-xs text-fail">{error}</p>}
        </div>
        <div><Button type="button" variant="outline" disabled={!text.trim() || locked} onClick={review}>문서 검토</Button></div>
      </FormSection>
      {reviewed && (
        <FormSection title="검토 결과">
          <Fields columns={3} items={[{ label: "이름", value: reviewed.name }, ...summarize(kind, reviewed.spec).map(([label, value]) => ({ label, value }))]} />
          <div><Button type="button" variant="default" loading={pending} disabled={locked} onClick={() => onRegister(reviewed)}>등록</Button></div>
        </FormSection>
      )}
    </div>
  )
}
