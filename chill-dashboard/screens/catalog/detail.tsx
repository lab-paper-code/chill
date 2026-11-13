"use client"

import * as React from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { catalogKinds, type CatalogKind } from "@/lib/routes"
import { useChillMutation, useChillQuery } from "@/lib/client"
import { ApiError } from "@/lib/client-transport"
import { resourceHref, formatText, type DatasetResource, type ModelResource, type PreprocessorResource, type RawField, type WorkloadSummary } from "@/lib/domain"
import { buildCatalogManifest } from "@/lib/catalog-manifest"
import { servicesUsing, type CatalogResource } from "@/lib/catalog-read"
import { serviceReadiness } from "@/lib/status-labels"
import { formatBytes } from "@/lib/format"
import { architectureLabel, executionPathLabel } from "@/lib/display-labels"
import { buttonClassName, ConfirmDialog, DataTable, Dialog, EmptyState, Fields, Hint, IdText, Page, PageHeader, QueryBoundary, RawDialog, Section, StatusBadge, TimeText, useToast } from "@/components/kit"
import Button from "@/components/custom/blocks/Button"

const none = <EmptyState title="없음" compact />

export function CatalogDetailScreen({ kind, name }: { kind: CatalogKind; name: string }) {
  const detail = useChillQuery<CatalogResource>(`v1/${kind}/${encodeURIComponent(name)}`)
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const [raw, setRaw] = React.useState(false)
  const [deleting, setDeleting] = React.useState(false)
  const router = useRouter()
  const queryClient = useQueryClient()
  const mutation = useChillMutation()
  const { toast } = useToast()
  const item = detail.data
  const users = item ? servicesUsing(workloads.data, kind, name, item.metadata.uid) : []
  const label = catalogKinds[kind]

  const download = () => {
    if (!item) return
    const text = JSON.stringify(buildCatalogManifest(kind, name, item.spec), null, 2)
    const url = URL.createObjectURL(new Blob([text], { type: "application/json" }))
    Object.assign(document.createElement("a"), { href: url, download: `${kind.slice(0, -1)}-${name}.json` }).click()
    URL.revokeObjectURL(url)
  }
  const remove = () => {
    if (!item) return
    mutation.mutate({ path: `v1/${kind}/${encodeURIComponent(name)}`, method: "DELETE", version: item.metadata.resourceVersion }, {
      onSuccess: () => {
        toast({ title: `${label} 삭제됨`, description: name, type: "success" })
        void queryClient.invalidateQueries({ queryKey: ["chill", `v1/${kind}`] })
        router.replace(`/catalog/${kind}`)
      },
      onError: error => {
        setDeleting(false)
        toast({ title: error instanceof ApiError && error.unknownOutcome ? "결과 미확인" : "삭제 실패", description: name, type: "error" })
      },
    })
  }

  return (
    <Page>
      <PageHeader
        title={name}
        actions={item && <Link href={`/catalog/${kind}/${encodeURIComponent(name)}/edit`} className={buttonClassName({ variant: "default" })}>수정</Link>}
        more={[
          { label: "등록 문서 내려받기", onSelect: download, disabled: !item },
          { label: "원본 보기", onSelect: () => setRaw(true), disabled: !item },
          { label: "삭제", destructive: true, onSelect: () => setDeleting(true), disabled: !item || !workloads.data },
        ]}
      />
      <QueryBoundary query={detail} skeleton="block">
        {data => (
          <Page>
            <Section card>
              <Fields columns={3} items={[
                { label: "등록", value: data.metadata.creationTimestamp ? <TimeText at={data.metadata.creationTimestamp} mode="detail" /> : null },
                { label: "사용 서비스", value: workloads.data ? `${users.length}개` : null, reason: "서비스 목록 조회 실패" },
                ...(kind === "datasets" ? [
                  { label: "이미지", value: <IdText value={(data as DatasetResource).spec.source.image} /> },
                  { label: "경로", value: (data as DatasetResource).spec.source.path },
                ] : []),
                ...(kind === "preprocessors" && (data as PreprocessorResource).spec.maxNewTokens != null ? [{ label: "최대 생성 토큰", value: (data as PreprocessorResource).spec.maxNewTokens }] : []),
              ]} />
            </Section>
            {kind === "models" && <ModelBody spec={(data as ModelResource).spec} />}
            {kind === "preprocessors" && <PreprocessorBody spec={(data as PreprocessorResource).spec} />}
            {kind === "datasets" && <DatasetBody spec={(data as DatasetResource).spec} />}
            <Section title="사용 서비스">
              <QueryBoundary query={workloads} skeleton="table">
                {() => <UsersTable users={users} />}
              </QueryBoundary>
            </Section>
            <RawDialog open={raw} onOpenChange={setRaw} value={data} filename={`${kind.slice(0, -1)}-${name}`} />
            {users.length > 0 ? (
              <Dialog open={deleting} onOpenChange={setDeleting} title="삭제할 수 없음" size="md" footer={<Button variant="outline" onClick={() => setDeleting(false)}>닫기</Button>}>
                <div className="grid gap-3">
                  <div className="text-sm"><span className="text-muted-foreground">대상 </span><span className="font-medium">{name}</span></div>
                  <UsersTable users={users} compact />
                </div>
              </Dialog>
            ) : (
              <ConfirmDialog open={deleting} onOpenChange={setDeleting} title={`${label} 삭제`} target={name} tone="destructive" confirmLabel="삭제" pending={mutation.isPending} onConfirm={remove} />
            )}
          </Page>
        )}
      </QueryBoundary>
    </Page>
  )
}

function UsersTable({ users, compact = false }: { users: WorkloadSummary[]; compact?: boolean }) {
  return (
    <DataTable
      label="사용 서비스"
      rows={users}
      rowKey={w => w.desired.metadata.uid}
      rowHref={w => resourceHref("services", w.desired.metadata.name, w.desired.metadata.uid)}
      empty={none}
      pageSize={0}
      density={compact ? "compact" : "default"}
      columns={[
        { key: "name", header: "서비스", cell: w => w.desired.metadata.name, sortValue: w => w.desired.metadata.name },
        { key: "status", header: "상태", cell: w => <StatusBadge status={serviceReadiness(w.readiness)} /> },
      ]}
    />
  )
}

function FieldsTable({ label, fields }: { label: string; fields: (Omit<RawField, "maxBytes"> & { maxBytes?: number })[] }) {
  return (
    <DataTable
      label={label}
      rows={fields}
      rowKey={f => f.name}
      empty={none}
      pageSize={0}
      columns={[
        { key: "name", header: "이름", cell: f => f.name },
        { key: "format", header: "형식", cell: f => formatText(f.format) },
        ...(fields.some(f => f.maxBytes != null) ? [{ key: "max", header: "최대 크기", numeric: true, cell: (f: { maxBytes?: number }) => f.maxBytes == null ? null : formatBytes(f.maxBytes) }] : []),
      ]}
    />
  )
}

function ModelBody({ spec }: { spec: ModelResource["spec"] }) {
  return (
    <>
      <Section title="파일">
        <DataTable
          label="모델 파일"
          rows={spec.artifacts}
          rowKey={a => a.name}
          empty={none}
          pageSize={0}
          columns={[
            { key: "name", header: "이름", cell: a => a.name },
            { key: "format", header: "형식", cell: a => a.format.toUpperCase() },
            { key: "arch", header: "아키텍처", cell: a => architectureLabel(a.source.architecture) },
            { key: "files", header: "파일", numeric: true, cell: a => a.source.files.length },
            { key: "image", header: "이미지", cell: a => <IdText value={a.source.image} /> },
          ]}
        />
      </Section>
      <Section title="실행 경로">
        <DataTable
          label="실행 경로"
          rows={spec.executionPaths}
          rowKey={p => p.name}
          empty={none}
          pageSize={0}
          columns={[
            { key: "name", header: "이름", cell: p => <Hint text={p.name}>{executionPathLabel(p.name, { spec })}</Hint> },
            { key: "artifact", header: "파일", cell: p => p.artifact },
            { key: "runtime", header: "런타임", cell: p => p.runtime.family },
            { key: "backend", header: "백엔드", cell: p => p.runtime.backend },
          ]}
        />
      </Section>
    </>
  )
}

function PreprocessorBody({ spec }: { spec: PreprocessorResource["spec"] }) {
  return (
    <>
      <Section title="입력"><FieldsTable label="입력 필드" fields={spec.input.fields} /></Section>
      <Section title="출력"><FieldsTable label="출력 필드" fields={spec.output.fields} /></Section>
      <Section title="대상 파일">
        <DataTable
          label="대상 파일"
          rows={spec.artifacts}
          rowKey={a => `${a.target.model}/${a.target.artifact}`}
          empty={none}
          pageSize={0}
          columns={[
            { key: "model", header: "모델", cell: a => a.target.model },
            { key: "artifact", header: "파일", cell: a => a.target.artifact },
            { key: "arch", header: "아키텍처", cell: a => architectureLabel(a.source.architecture) },
            { key: "path", header: "경로", cell: a => a.source.path },
            { key: "image", header: "이미지", cell: a => <IdText value={a.source.image} /> },
          ]}
        />
      </Section>
    </>
  )
}

function DatasetBody({ spec }: { spec: DatasetResource["spec"] }) {
  return <Section title="필드"><FieldsTable label="필드" fields={spec.fields} /></Section>
}
