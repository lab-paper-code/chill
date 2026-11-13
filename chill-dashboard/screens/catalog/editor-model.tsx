"use client"

import { FormSection } from "@/components/kit"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { backendsByFormat, datatypes, type ExecutionPath, type ModelArtifact, type ModelSpec } from "@/lib/domain"
import { blankArtifact, blankPath, blankTensor, type Errors } from "@/lib/catalog-editor"
import { IntList, Num, PathList, Pick, RowList, Text } from "./editor-fields"

const formats = Object.keys(backendsByFormat).map(value => ({ value }))
const architectures = [{ value: "amd64" }, { value: "arm64" }]
const policies = [{ value: "keep", label: "유지" }, { value: "drop", label: "버림" }, { value: "defer", label: "미룸" }]
const derivationBlank = { sourceArtifact: "", targetPlatform: "", declaredBatchSizes: [1], conversion: { tool: "", version: "" } }

export function ModelForm({ spec, onChange, errors }: { spec: ModelSpec; onChange: (spec: ModelSpec) => void; errors: Errors }) {
  const artifactNames = spec.artifacts.map(a => a.name).filter(Boolean)

  // Renaming or changing the format of a file keeps the paths that run it consistent.
  function setArtifact(index: number, patch: Partial<ModelArtifact>) {
    const before = spec.artifacts[index]
    const next = { ...before, ...patch }
    if (patch.format && patch.format !== before.format) {
      if (patch.format === "rknn") next.input = before.input ?? { tensors: [blankTensor()] }
      else delete next.input
    }
    const artifacts = spec.artifacts.map((a, i) => i === index ? next : a)
    const executionPaths = spec.executionPaths.map(p => {
      if (p.artifact !== before.name) return p
      const allowed = backendsByFormat[next.format] ?? []
      const backend = allowed.includes(p.runtime.backend) || !patch.format ? p.runtime.backend : allowed[0] ?? ""
      return { ...p, artifact: next.name, runtime: { ...p.runtime, backend } }
    })
    onChange({ artifacts, executionPaths })
  }
  const setPath = (index: number, patch: Partial<ExecutionPath>) => onChange({ ...spec, executionPaths: spec.executionPaths.map((p, i) => i === index ? { ...p, ...patch } : p) })

  return (
    <>
      <FormSection title="모델 파일">
        <RowList title="파일" items={spec.artifacts} onChange={artifacts => onChange({ ...spec, artifacts })} blank={blankArtifact} error={errors.artifacts}
          render={(a, i) => {
            const p = `artifacts.${i}`
            return (
              <>
                <div className="grid gap-3 sm:grid-cols-3">
                  <Text label="이름" value={a.name} onChange={name => setArtifact(i, { name })} error={errors[`${p}.name`]} />
                  <Pick label="형식" value={a.format} options={formats} onChange={format => setArtifact(i, { format })} error={errors[`${p}.format`]} />
                  <Pick label="아키텍처" value={a.source.architecture} options={architectures} onChange={architecture => setArtifact(i, { source: { ...a.source, architecture } })} error={errors[`${p}.architecture`]} />
                </div>
                <Text label="이미지" value={a.source.image} onChange={image => setArtifact(i, { source: { ...a.source, image } })} error={errors[`${p}.image`]} placeholder="registry/name@sha256:…" />
                <PathList label="파일 경로" items={a.source.files} onChange={files => setArtifact(i, { source: { ...a.source, files } })} errors={errors} prefix={`${p}.files`} />
                {a.format === "rknn" && (
                  <RowList title="입력 텐서" items={a.input?.tensors ?? []} onChange={tensors => setArtifact(i, { input: { tensors } })} blank={blankTensor} error={errors[`${p}.tensors`]}
                    render={(t, j, set) => (
                      <div className="grid gap-3 sm:grid-cols-3">
                        <Text label="이름" value={t.name} onChange={name => set({ name })} error={errors[`${p}.tensors.${j}.name`]} />
                        <Pick label="자료형" value={t.datatype} options={datatypes.map(value => ({ value }))} onChange={datatype => set({ datatype: datatype as typeof t.datatype })} />
                        <IntList label="형태" value={t.sampleShape} onChange={sampleShape => set({ sampleShape })} error={errors[`${p}.tensors.${j}.shape`]} />
                      </div>
                    )} />
                )}
                <div className="flex items-center gap-2">
                  <Switch id={`derived-${i}`} checked={!!a.derivation} onCheckedChange={on => setArtifact(i, { derivation: on ? structuredClone(derivationBlank) : undefined })} />
                  <Label htmlFor={`derived-${i}`}>다른 파일에서 변환</Label>
                </div>
                {a.derivation && (
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Pick label="원본 파일" value={a.derivation.sourceArtifact} options={artifactNames.filter(n => n !== a.name).map(value => ({ value }))} onChange={sourceArtifact => setArtifact(i, { derivation: { ...a.derivation!, sourceArtifact } })} error={errors[`${p}.derivation.source`]} />
                    <Text label="대상 플랫폼" value={a.derivation.targetPlatform} onChange={targetPlatform => setArtifact(i, { derivation: { ...a.derivation!, targetPlatform } })} error={errors[`${p}.derivation.platform`]} />
                    <IntList label="배치 크기" value={a.derivation.declaredBatchSizes} onChange={declaredBatchSizes => setArtifact(i, { derivation: { ...a.derivation!, declaredBatchSizes } })} error={errors[`${p}.derivation.batches`]} />
                    <Text label="변환 도구" value={a.derivation.conversion.tool} onChange={tool => setArtifact(i, { derivation: { ...a.derivation!, conversion: { ...a.derivation!.conversion, tool } } })} error={errors[`${p}.derivation.tool`]} />
                    <Text label="도구 버전" value={a.derivation.conversion.version} onChange={version => setArtifact(i, { derivation: { ...a.derivation!, conversion: { ...a.derivation!.conversion, version } } })} error={errors[`${p}.derivation.version`]} />
                  </div>
                )}
              </>
            )
          }} />
      </FormSection>
      <FormSection title="실행 경로">
        <RowList title="경로" items={spec.executionPaths} onChange={executionPaths => onChange({ ...spec, executionPaths })} blank={() => blankPath(artifactNames[0])} error={errors.executionPaths}
          render={(x, i) => {
            const p = `executionPaths.${i}`
            const format = spec.artifacts.find(a => a.name === x.artifact)?.format ?? ""
            const allowed = backendsByFormat[format] ?? []
            const backends = allowed.includes(x.runtime.backend) || !x.runtime.backend ? allowed : [...allowed, x.runtime.backend]
            const ff = x.runtime.settings?.firstfloor
            const setFirstfloor = (patch: Partial<NonNullable<typeof ff>>) => setPath(i, { runtime: { ...x.runtime, settings: { ...x.runtime.settings, firstfloor: { maxBatchSize: 1, maxQueueSize: 1, ...ff, ...patch } } } })
            return (
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                <Text label="이름" value={x.name} onChange={name => setPath(i, { name })} error={errors[`${p}.name`]} />
                <Pick label="모델 파일" value={x.artifact} options={artifactNames.map(value => ({ value }))} onChange={artifact => setPath(i, { artifact })} error={errors[`${p}.artifact`]} />
                <Pick label="백엔드" value={x.runtime.backend} options={backends.map(value => ({ value }))} onChange={backend => setPath(i, { runtime: { ...x.runtime, backend } })} error={errors[`${p}.backend`]} />
                <Num label="최대 배치" value={ff?.maxBatchSize} onChange={maxBatchSize => setFirstfloor({ maxBatchSize })} error={errors[`${p}.batch`]} />
                <Num label="최대 대기열" value={ff?.maxQueueSize} onChange={maxQueueSize => setFirstfloor({ maxQueueSize })} error={errors[`${p}.queue`]} />
                <Pick label="기한 초과" value={ff?.deadlinePolicy ?? "keep"} options={policies} onChange={deadlinePolicy => setFirstfloor({ deadlinePolicy: deadlinePolicy as "keep" | "drop" | "defer" })} />
              </div>
            )
          }} />
      </FormSection>
    </>
  )
}
