"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { catalogKinds, type CatalogKind } from "@/lib/routes"
import { useChillQuery } from "@/lib/client"
import { errorCodeLabel } from "@/lib/status-labels"
import type { ModelList } from "@/lib/domain"
import { blankDraft, diff, draftFrom, validate, type Draft } from "@/lib/catalog-editor"
import { useResource, type ResourceState } from "@/hooks/use-catalog-resource"
import { Button, buttonClassName, ConfirmDialog, EmptyState, Fields, FormActions, LeaveGuard, Page, PageHeader, PageTabs, QueryBoundary, useToast } from "@/components/kit"
import { Alert } from "@/components/custom/blocks/Alerts"
import { Text } from "./editor-fields"
import { DatasetForm, PreprocessorForm } from "./editor-io"
import { ImportTab } from "./editor-import"
import { ModelForm } from "./editor-model"

export function CatalogEditorScreen({ kind, name }: { kind: CatalogKind; name?: string }) {
  const r = useResource(kind, name)
  const label = catalogKinds[kind]
  const title = name ? `${name} 수정` : `${label} 등록`
  if (!name) return <Editor kind={kind} r={r} title={title} />
  if (!r.source) return <Page><PageHeader title={title} /><QueryBoundary query={r.detail} skeleton="block">{() => null}</QueryBoundary></Page>
  if (r.wrongUID || r.identityChanged || r.deletedUID) {
    return <Page><PageHeader title={title} /><EmptyState title="대상이 바뀜" action={<Link href={`/catalog/${kind}`} className={buttonClassName({ variant: "outline" })}>목록</Link>} /></Page>
  }
  return <Editor key={r.source.metadata.uid} kind={kind} r={r} name={name} title={title} />
}

function Editor({ kind, r, name, title }: { kind: CatalogKind; r: ResourceState; name?: string; title: string }) {
  const router = useRouter()
  const { toast } = useToast()
  const search = useSearchParams()
  const isNew = !name
  const importing = isNew && search.get("tab") === "import"
  const models = useChillQuery<ModelList>("v1/models", { enabled: kind === "preprocessors" })
  const [draft, setDraft] = useState<Draft | null>(() => r.source ? draftFrom(kind, r.source.spec) : blankDraft(kind))
  const [newName, setNewName] = useState("")
  const [docText, setDocText] = useState("")
  const [attempted, setAttempted] = useState(false)
  const [review, setReview] = useState(false)

  const result = r.result, error = r.mutation.error
  const [imported, setImported] = useState("")
  const target = !isNew ? name : importing ? imported : newName.trim()
  useEffect(() => {
    if (!result) return
    toast({ title: isNew ? "등록됨" : "적용됨", description: target, type: "success" })
    router.push(`/catalog/${kind}/${encodeURIComponent(target ?? "")}`)
  }, [result]) // eslint-disable-line react-hooks/exhaustive-deps -- once per saved result
  useEffect(() => {
    if (!error) return
    const code = (error.data as { code?: string } | null)?.code
    toast({ title: error.unknownOutcome ? "결과 미확인" : "저장 실패", description: error.unknownOutcome ? "최신 상태 확인 필요" : code ? errorCodeLabel(code) : error.message, type: "error" })
  }, [error]) // eslint-disable-line react-hooks/exhaustive-deps -- once per failure

  if (!draft) return <Page><PageHeader title={title} /><EmptyState title="수정할 수 없는 형식" /></Page>

  const errors = attempted ? validate(draft, newName, isNew) : {}
  const changes = r.source ? diff(r.source.spec, draft.spec) : []
  const dirty = isNew ? !!newName || !!docText || JSON.stringify(draft.spec) !== JSON.stringify(blankDraft(kind).spec) : changes.length > 0
  const locked = r.blocked || r.conflict
  const send = (body: unknown) => r.submit(isNew ? "POST" : "PUT", body)

  function submit(event: React.FormEvent) {
    event.preventDefault()
    if (!draft || locked) return
    setAttempted(true)
    if (Object.keys(validate(draft, newName, isNew)).length) {
      toast({ title: "입력 확인", type: "warning" })
      requestAnimationFrame(() => document.querySelector<HTMLElement>("[aria-invalid='true']")?.focus())
      return
    }
    if (isNew) void send({ name: newName.trim(), spec: draft.spec })
    else setReview(true)
  }

  return (
    <Page>
      <PageHeader title={title} />
      {isNew && <PageTabs param="tab" defaultValue="form" tabs={[{ value: "form", label: "폼" }, { value: "import", label: "문서 가져오기" }]} />}
      <LeaveGuard when={dirty && !result} />
      {r.conflict && (
        <Alert type="warning" icon="AlertTriangle" title="다른 곳에서 먼저 수정됨">
          <Button type="button" size="sm" variant="outline" disabled={!r.current || r.mutation.isPending} onClick={r.adopt}>최신 기준으로 계속</Button>
        </Alert>
      )}
      {error?.unknownOutcome && (
        <Alert type="danger" icon="XCircle" title="결과 미확인">
          <Button type="button" size="sm" variant="outline" onClick={() => void r.detail.refetch()} disabled={isNew}>최신 상태 확인</Button>
          {isNew && <Link href={`/catalog/${kind}`} className={buttonClassName({ variant: "outline", size: "sm" })}>목록 확인</Link>}
        </Alert>
      )}
      {importing ? (
        <ImportTab kind={kind} text={docText} onText={setDocText} locked={locked} pending={r.mutation.isPending} onRegister={registration => { setImported(registration.name); void send(registration) }} />
      ) : (
        <form className="grid gap-4" onSubmit={submit} noValidate>
          {isNew && (
            <section aria-label="이름" className="rounded-xl border border-border bg-card p-5">
              <Text label="이름" value={newName} onChange={setNewName} error={errors.name} />
            </section>
          )}
          {draft.kind === "models" && <ModelForm spec={draft.spec} errors={errors} onChange={spec => setDraft({ kind: "models", spec })} />}
          {draft.kind === "preprocessors" && <PreprocessorForm spec={draft.spec} errors={errors} models={models.data?.items ?? []} onChange={spec => setDraft({ kind: "preprocessors", spec })} />}
          {draft.kind === "datasets" && <DatasetForm spec={draft.spec} errors={errors} onChange={spec => setDraft({ kind: "datasets", spec })} />}
          <FormActions changes={isNew ? undefined : changes.length}>
            <Link href={isNew ? `/catalog/${kind}` : `/catalog/${kind}/${encodeURIComponent(name ?? "")}`} className={buttonClassName({ variant: "ghost" })}>취소</Link>
            <Button type="submit" variant="default" loading={r.mutation.isPending && !review} disabled={locked || (!isNew && !changes.length)}>{isNew ? "등록" : "검토 후 적용"}</Button>
          </FormActions>
        </form>
      )}
      <ConfirmDialog open={review} onOpenChange={setReview} title="변경 검토" target={name} confirmLabel="적용" pending={r.mutation.isPending}
        changes={changes} onConfirm={() => { void send({ spec: draft.spec }).then(() => setReview(false)) }}>
        {!!r.affected?.length && <Fields columns={1} items={[{ label: "사용 서비스", value: r.affected.map(w => w.desired.metadata.name).join(", ") }]} />}
      </ConfirmDialog>
    </Page>
  )
}
