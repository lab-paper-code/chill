"use client"

import { FormSection } from "@/components/kit"
import { architectureLabel } from "@/lib/display-labels"
import { fieldFormats, formatText, type DatasetSpec, type ModelResource, type PreprocessorSpec, type RawField } from "@/lib/domain"
import { blankField, blankTarget, type Errors } from "@/lib/catalog-editor"
import { Num, Pick, RowList, Text } from "./editor-fields"

const formats = fieldFormats.map(value => ({ value, label: formatText(value) }))
const architectures = ["amd64", "arm64"].map(value => ({ value, label: architectureLabel(value) }))

function FieldRows<T extends { name: string; format: RawField["format"]; maxBytes?: number }>({ title, fields, onChange, blank, errors, prefix, sized }: { title: string; fields: T[]; onChange: (fields: T[]) => void; blank: () => T; errors: Errors; prefix: string; sized: boolean }) {
  return (
    <RowList title={title} items={fields} onChange={onChange} blank={blank} error={errors[prefix]}
      render={(f, i, set) => (
        <div className={sized ? "grid gap-3 sm:grid-cols-3" : "grid gap-3 sm:grid-cols-2"}>
          <Text label="이름" value={f.name} onChange={name => set({ name } as Partial<T>)} error={errors[`${prefix}.${i}.name`]} />
          <Pick label="형식" value={f.format} options={formats} onChange={format => set({ format: format as RawField["format"] } as Partial<T>)} />
          {sized && <Num label="최대 크기 (B)" value={f.maxBytes} onChange={maxBytes => set({ maxBytes } as Partial<T>)} error={errors[`${prefix}.${i}.maxBytes`]} />}
        </div>
      )} />
  )
}

export function PreprocessorForm({ spec, onChange, errors, models }: { spec: PreprocessorSpec; onChange: (spec: PreprocessorSpec) => void; errors: Errors; models: ModelResource[] }) {
  return (
    <>
      <FormSection title="입력">
        <FieldRows title="필드" fields={spec.input.fields} onChange={fields => onChange({ ...spec, input: { fields } })} blank={blankField} errors={errors} prefix="input" sized />
      </FormSection>
      <FormSection title="출력">
        <FieldRows title="필드" fields={spec.output.fields} onChange={fields => onChange({ ...spec, output: { fields } })} blank={() => ({ name: "", format: "UTF8" })} errors={errors} prefix="output" sized={false} />
        <Num label="최대 생성 토큰 (선택)" value={spec.maxNewTokens} onChange={value => onChange({ ...spec, maxNewTokens: Number.isNaN(value) ? undefined : value })} error={errors.maxNewTokens} />
      </FormSection>
      <FormSection title="대상 모델 파일">
        <RowList title="대상" items={spec.artifacts} onChange={artifacts => onChange({ ...spec, artifacts })} blank={blankTarget} error={errors.artifacts}
          render={(a, i, set) => {
            const p = `artifacts.${i}`
            const names = models.map(m => m.metadata.name)
            const files = models.find(m => m.metadata.name === a.target.model)?.spec.artifacts.map(f => f.name) ?? []
            return (
              <>
                <div className="grid gap-3 sm:grid-cols-2">
                  <Pick label="모델" value={a.target.model} options={[...new Set([...names, ...(a.target.model ? [a.target.model] : [])])].map(value => ({ value }))} onChange={model => set({ target: { model, artifact: "" } })} error={errors[`${p}.model`]} />
                  <Pick label="모델 파일" value={a.target.artifact} options={[...new Set([...files, ...(a.target.artifact ? [a.target.artifact] : [])])].map(value => ({ value }))} onChange={artifact => set({ target: { ...a.target, artifact } })} error={errors[`${p}.artifact`]} />
                </div>
                <Text label="이미지" value={a.source.image} onChange={image => set({ source: { ...a.source, image } })} error={errors[`${p}.image`]} placeholder="registry/name@sha256:…" />
                <div className="grid gap-3 sm:grid-cols-2">
                  <Pick label="아키텍처" value={a.source.architecture} options={architectures} onChange={architecture => set({ source: { ...a.source, architecture } })} error={errors[`${p}.architecture`]} />
                  <Text label="경로" value={a.source.path} onChange={path => set({ source: { ...a.source, path } })} error={errors[`${p}.path`]} placeholder="/artifact/processor" />
                </div>
              </>
            )
          }} />
      </FormSection>
    </>
  )
}

export function DatasetForm({ spec, onChange, errors }: { spec: DatasetSpec; onChange: (spec: DatasetSpec) => void; errors: Errors }) {
  return (
    <>
      <FormSection title="보관 위치">
        <Text label="이미지" value={spec.source.image} onChange={image => onChange({ ...spec, source: { ...spec.source, image } })} error={errors.image} placeholder="registry/name@sha256:…" />
        <Text label="경로" value={spec.source.path} onChange={path => onChange({ ...spec, source: { ...spec.source, path } })} error={errors.path} placeholder="/artifact/dataset" />
      </FormSection>
      <FormSection title="필드">
        <FieldRows title="필드" fields={spec.fields} onChange={fields => onChange({ ...spec, fields })} blank={blankField} errors={errors} prefix="fields" sized />
      </FormSection>
    </>
  )
}
