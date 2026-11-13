"use client"

import * as React from "react"
import { Pencil, Plus } from "lucide-react"
import { ApiError } from "@/lib/client-transport"
import { sectionTabs } from "@/lib/routes"
import { formatPercent, formatRate } from "@/lib/format"
import { autonomyState, serviceReadiness, sloVerdict, type StatusTone } from "@/lib/status-labels"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import {
  Button, ConfirmDialog, DataTable, DetailDrawer, Dialog, EmptyState, Feed, Fields, FilterBar, FormActions, FormSection, HeadroomBar, IdText, LeaveGuard, Matrix, MatrixCell, MeterBar,
  Page, PageHeader, PageTabs, QueryBoundary, Rail, RawDialog, Section, Select, Sparkline, StatRow, StatTile, StatusBadge, StatusDot, Steps, TimeRange, TimeSeries, TimeText,
  useTimeRangeParam, useToast, type Column, type SeriesPoint,
} from "@/components/kit"

const BASE = Date.parse("2026-11-13T05:00:00Z")
const devices = ["jetson-nano", "jetson-orin-nx", "rk3588-a", "rk3588-b", "xeon-gpu-01"]
type ServiceRow = { name: string; tone: StatusTone; rate: number; trend: (number | null)[]; latency: number | null; target: number; success: number | null; nodes: string[]; autonomy: string }
const names = ["image-classification-tight-slo", "image-classification-relaxed", "object-detection-edge", "vqa-llava-batch", "speech-to-text-small", "text-embedding-bge", "pose-estimation-live", "ocr-receipts", "face-blur-stream", "summarize-ko-news", "translation-ko-en", "anomaly-detector"]
const rows: ServiceRow[] = names.map((name, index) => ({
  name,
  tone: (["ok", "ok", "warn", "fail", "progress", "idle"] as const)[index % 6],
  rate: index % 6 === 5 ? 0 : Math.round((40 + index * 13.7) * 10) / 10,
  trend: Array.from({ length: 60 }, (_, i) => (index === 3 && i > 30 && i < 38 ? null : 20 + Math.sin((i + index) / 6) * 8 + index)),
  latency: index % 6 === 5 ? null : 120 + index * 47,
  target: 500,
  success: index % 6 === 5 ? null : 0.9 + (index % 5) / 50,
  nodes: devices.slice(index % 3, (index % 3) + 1 + (index % 4)),
  autonomy: ["Controlled", "Fixed", "Disabled", "MeasurementEvidenceMissing"][index % 4],
}))
const toneLabel: Record<StatusTone, string> = { ok: "정상", warn: "주의", fail: "실패", progress: "진행", idle: "유휴" }

const columns: Column<ServiceRow>[] = [
  { key: "status", header: "상태", width: "6.5rem", cell: row => <StatusDot tone={row.tone} label={toneLabel[row.tone]} showLabel />, sortValue: row => row.tone },
  { key: "name", header: "서비스", width: "26%", cell: row => row.name, sortValue: row => row.name },
  { key: "rate", header: "유입", unit: "req/s", numeric: true, cell: row => <span className="inline-flex items-center gap-2"><Sparkline values={row.trend} label="최근 60초 유입" />{row.rate.toFixed(1)}</span>, sortValue: row => row.rate },
  { key: "latency", header: "지연", unit: "ms", numeric: true, missingReason: "응답 없음", missing: row => row.tone === "idle" ? "유휴 서비스" : undefined, cell: row => row.latency == null ? null : <span className="inline-flex items-center gap-2"><HeadroomBar observed={row.latency} target={row.target} />{row.latency}</span>, sortValue: row => row.latency },
  { key: "success", header: "최근 60초 요청 성공률", unit: "%", numeric: true, missingReason: "요청 없음", cell: row => row.success == null ? null : (row.success * 100).toFixed(1), sortValue: row => row.success },
  { key: "nodes", header: "배치", cell: row => row.nodes.join(", "), csv: row => row.nodes.join(" ") },
  { key: "autonomy", header: "자동 제어", cell: row => <StatusBadge status={autonomyState(row.autonomy)} />, sortValue: row => row.autonomy },
]

const series: SeriesPoint[] = Array.from({ length: 900 }, (_, i) => {
  const t = BASE - (900 - i) * 1000
  const gap = i > 420 && i < 470
  return { t, whole: gap ? null : 180 + Math.sin(i / 40) * 25, active: gap ? null : 95 + Math.cos(i / 55) * 18 }
})
// A low rate (0 to 1.1 req/s) with one sample between two gaps, and a load scenario in seconds.
const lowRate: SeriesPoint[] = Array.from({ length: 120 }, (_, i) => ({ t: BASE - (120 - i) * 5000, rate: i > 50 && i < 70 ? (i === 60 ? 0.4 : null) : 0.6 + Math.sin(i / 9) * 0.5 }))
const scenario: SeriesPoint[] = [0, 60, 120, 240, 300, 420, 600].map((t, i) => ({ t, a: [2, 5, 8, 8, 3, 0, 0][i], b: [1, 1, 4, 6, 6, 2, 2][i] }))
const paths = [{ key: "cuda-fp16", label: "CUDA FP16" }, { key: "cuda-int8", label: "CUDA INT8", hint: "TensorRT INT8 엔진" }, { key: "cpu-fp32", label: "CPU FP32" }]
const groups = [
  { key: "orin", label: "Jetson Orin NX", rows: ["MAXN", "25W", "15W"].map((mode, i) => ({ key: mode, label: mode, cells: { "cuda-fp16": i, "cuda-int8": i === 2 ? undefined : i + 1, "cpu-fp32": 3 } })) },
  { key: "rk3588", label: "RK3588", rows: [{ key: "performance", label: "performance", cells: { "cpu-fp32": 0 } as Record<string, number | undefined> }] },
]
const matrixTones = ["ok", "progress", "warn", "idle"] as const

const pending = { data: undefined, error: null, isPending: true, refetch: () => {} }
const failed = { data: undefined, error: new ApiError(503, "", { code: "SchedulerUnavailable" }), isPending: false, refetch: () => {} }
const denied = { data: undefined, error: new ApiError(403, "", { code: "Forbidden" }), isPending: false, refetch: () => {} }
const warming = { data: undefined, error: null, isPending: true, failureReason: new ApiError(503, "", { code: "CoverageWarming" }), refetch: () => {} }
const emptyList = { data: [] as string[], error: null, isPending: false, refetch: () => {} }

export function KitShowcase() {
  const { toast } = useToast()
  const [raw, setRaw] = React.useState(false)
  const [confirm, setConfirm] = React.useState(false)
  const [compare, setCompare] = React.useState(false)
  const [drawer, setDrawer] = React.useState<ServiceRow | null>(null)
  const [search, setSearch] = React.useState("")
  const [state, setState] = React.useState<string[]>([])
  const [group, setGroup] = React.useState("targets")
  const [picked, setPicked] = React.useState("")
  const [outcomes, setOutcomes] = React.useState<string[]>([])
  const [device, setDevice] = React.useState("")
  const [range, setRange] = useTimeRangeParam(["15m", "1h"], "15m", 3_600_000)
  const [dirty, setDirty] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const visible = rows.filter(row => (!search || row.name.includes(search)) && (state.length === 0 || state.includes(row.tone)) && (!device || row.nodes.includes(device)))
  return (
    <Page>
      <PageHeader
        title="image-classification-tight-slo"
        status={serviceReadiness("Ready")}
        badges={<StatusBadge tone="warn" label="접수 중지" />}
        actions={<><Button disabled disabledReason="권한 없음">배치 변경</Button><Button variant="default" iconLeft={<Pencil />}>수정</Button></>}
        more={[{ label: "원본 보기", onSelect: () => setRaw(true) }, { label: "비교", onSelect: () => setCompare(true) }, { label: "복제", disabled: true, disabledReason: "권한 없음", onSelect: () => {} }, { label: "삭제", destructive: true, onSelect: () => setConfirm(true) }]}
      />
      <PageTabs param="tab" defaultValue="overview" tabs={[{ value: "overview", label: "현황" }, { value: "requests", label: "요청" }, { value: "definition", label: "정의" }, { value: "test", label: "테스트" }]} />

      <StatRow>
        <StatTile label="서비스" value="9 / 11" sub="목표 충족 / 판정" href="/services" info="최근 60초 서버 판정" />
        <StatTile label="유입" value={formatRate(482.4).replace(" req/s", "")} unit="req/s" sub="12개 서비스" />
        <StatTile label="전력" value="214.6" unit="W" sub="전체 풀 388.2 W" status={{ label: "정상", tone: "ok" }} />
        <StatTile label="스케줄러" value={null} reason="판단 기록 없음" sub={<TimeText at={BASE - 95_000} />} />
      </StatRow>

      <Section title="상태" card>
        <div className="flex flex-wrap items-center gap-2">
          {(["ok", "warn", "fail", "progress", "idle"] as const).map(tone => <StatusBadge key={tone} tone={tone} label={toneLabel[tone]} />)}
          <StatusBadge status={serviceReadiness("SomethingNew")} />
          <StatusBadge status={sloVerdict(true)} />
          <span className="mx-2 h-5 w-px bg-border" />
          {(["ok", "warn", "fail", "progress", "idle"] as const).map(tone => <StatusDot key={tone} tone={tone} label={toneLabel[tone]} />)}
        </div>
        <div className="mt-4 flex flex-wrap items-center gap-6 text-sm">
          <span className="inline-flex items-center gap-2"><HeadroomBar observed={210} target={500} />210 ms</span>
          <span className="inline-flex items-center gap-2"><HeadroomBar observed={430} target={500} />430 ms</span>
          <span className="inline-flex items-center gap-2"><HeadroomBar observed={610} target={500} />610 ms</span>
          <span className="inline-flex items-center gap-2"><HeadroomBar observed={null} target={500} reason="관측 없음" /></span>
          <IdText value="5f0c2a9e-41b7-4d3e-9a8f-1c2b3d4e5f60" />
          <TimeText at={BASE - 180_000} />
          <TimeText at={BASE} mode="absolute" />
        </div>
      </Section>

      <Section title="정의" card>
        <Fields items={[
          { label: "모델", value: "resnet50-int8" }, { label: "전처리기", value: "imagenet-224" },
          { label: "응답 목표", value: "p99.5 500 ms", info: "요청의 99.5%가 500 ms 안에 끝나야 함" }, { label: "데이터셋", value: null, reason: "지정 안 됨" },
          { label: "대기 상한", value: "64" }, { label: "생성", value: <TimeText at={BASE - 86_400_000 * 3} mode="absolute" /> },
        ]} />
      </Section>

      <DataTable
        label="서비스"
        rows={visible}
        columns={columns}
        rowKey={row => row.name}
        onRowClick={setDrawer}
        rowActions={row => [{ label: "상세", onSelect: () => setDrawer(row) }, { label: "삭제", destructive: true, disabled: true, disabledReason: "권한 없음", onSelect: () => {} }]}
        selectedKey={drawer?.name}
        defaultSort={{ key: "name", desc: false }}
        pageSize={10}
        download={{ filename: "services" }}
        busy={busy}
        toolbar={<FilterBar
          search={{ value: search, onChange: setSearch, placeholder: "서비스 이름" }}
          segments={[{ label: "상태", multiple: true, value: state, onChange: setState, options: [{ value: "fail", label: "실패", count: rows.filter(r => r.tone === "fail").length }, { value: "warn", label: "주의" }, { value: "ok", label: "정상" }] }]}
          selects={[{ label: "장치", value: device, onChange: setDevice, options: [{ value: "", label: "모든 장치" }, ...devices.map(value => ({ value, label: value }))] }]}
        />}
        actions={<Button size="sm" variant="ghost" onClick={() => { setBusy(true); window.setTimeout(() => setBusy(false), 800) }}>갱신</Button>}
      />

      <DataTable label="빈 표" rows={[] as ServiceRow[]} columns={columns.slice(0, 4)} rowKey={row => row.name} empty={<EmptyState compact title="조건에 맞는 서비스 없음" action={<Button size="sm" onClick={() => setSearch("")}>검색 지우기</Button>} />} />

      <div className="grid gap-4 lg:grid-cols-2">
        <Section title="불러오는 중" card><QueryBoundary query={pending}>{() => null}</QueryBoundary></Section>
        <Section title="초기화 중" card><QueryBoundary query={warming}>{() => null}</QueryBoundary></Section>
        <Section title="조회 실패" card><QueryBoundary query={failed}>{() => null}</QueryBoundary></Section>
        <Section title="권한 없음" card><QueryBoundary query={denied}>{() => null}</QueryBoundary></Section>
        <Section title="빈 결과" card><QueryBoundary query={emptyList} isEmpty={data => data.length === 0} empty={<EmptyState title="측정 요청 없음" action={<Button size="sm" variant="default" iconLeft={<Plus />}>측정 요청</Button>} />}>{() => null}</QueryBoundary></Section>
        <Section title="준비 중" card><EmptyState title="준비 중" /></Section>
      </div>

      <Section title="전체 전력" card actions={<TimeRange value={range} onChange={setRange} presets={["15m", "1h"]} custom={{ maxSpanMs: 3_600_000 }} />}>
        <TimeSeries label="최근 15분 전력, 5분 전부터 50초간 수집 없음" data={series} series={[{ key: "whole", label: "전체 풀" }, { key: "active", label: "활성 풀" }]} unit="W" reference={{ value: 200, label: "200 W" }} legend />
      </Section>

      <div className="grid gap-4 lg:grid-cols-2">
        <Section title="낮은 유입" card>
          <TimeSeries label="최근 10분 유입" data={lowRate} series={[{ key: "rate", label: "유입" }]} unit="req/s" reference={{ value: 1.5, label: "목표 1.5" }} height={180} />
        </Section>
        <Section title="부하 시나리오" card>
          <TimeSeries label="서비스별 요청률" data={scenario} series={[{ key: "a", label: "image-classification-tight-slo" }, { key: "b", label: "object-detection-edge" }]} x="seconds" curve="step" unit="req/s" legend height={180} />
        </Section>
      </div>

      <StatRow columns={3}>
        <StatTile label="전력 한도 사용" value="18.4" unit="W" sub={<MeterBar value={18.4} max={30} marker={25} label="전력 18.4 / 30 W" width="full" />} />
        <StatTile label="대기열" value="12" sub={<MeterBar value={12} max={64} tone="warn" label="대기 12 / 64" width="full" />} />
        <StatTile label="측정 범위" value={null} reason="측정 없음" sub={<MeterBar value={null} max={10} label="측정" reason="측정 없음" />} />
      </StatRow>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <Section title="진행" card>
          <Steps label="실험 진행" items={[{ label: "준비", status: "done" }, { label: "부하 주입", status: "done" }, { label: "측정", status: "failed", reason: "부하 생성 품질 미달" }, { label: "정리", status: "current" }, { label: "게시", status: "pending" }]} />
        </Section>
        <Section title="최근 변경" card>
          <Feed label="최근 변경" items={[
            { key: "1", title: "배치 변경", tone: "ok", secondary: "image-classification-tight-slo", at: BASE - 95_000, href: "/scheduler/actions" },
            { key: "2", title: "설정 변경", tone: "progress", secondary: "자동 제어 대상", at: BASE - 3_600_000, href: "/scheduler/settings" },
            { key: "3", title: "작업 실패", tone: "fail", secondary: "vqa-llava-batch", at: BASE - 7_200_000 },
          ]} />
        </Section>
      </div>

      <Section title="설정" card>
        <Rail label="설정 묶음" value={group} onChange={setGroup} items={[{ value: "targets", label: "대상", count: 2 }, { value: "limits", label: "한도" }, { value: "timing", label: "주기" }, { value: "power", label: "전력 모드" }, { value: "excluded", label: "제외 장치", count: 1 }]}>
          <Fields items={[{ label: "묶음", value: group }, { label: "변경", value: null, reason: "변경 없음" }]} />
        </Rail>
      </Section>

      <Matrix
        label="측정 범위" rowHeader="전력 모드" columns={paths} missingReason="조합 없음"
        groups={groups.map(item => ({ ...item, rows: item.rows.map(row => ({ ...row, cells: Object.fromEntries(Object.entries(row.cells).flatMap(([path, index]) => index == null ? [] : [[path,
          <MatrixCell key={path} label={`${row.label} ${path} ${toneLabel[matrixTones[index]]}`} onClick={() => toast({ title: `${row.label} ${path}`, type: "info" })}><StatusDot tone={matrixTones[index]} label={index ? `${index}/4` : ""} showLabel /></MatrixCell>,
        ]])) })) }))}
        legend={<ul aria-label="범례" className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">{matrixTones.map(tone => <li key={tone}><StatusDot tone={tone} label={toneLabel[tone]} showLabel /></li>)}</ul>}
      />

      <form className="grid gap-4" onSubmit={event => { event.preventDefault(); toast({ title: "저장됨", type: "success" }); setDirty(false) }}>
        <div className="grid gap-4 lg:grid-cols-2">
          <FormSection title="이름">
            <InputBlock label="서비스 이름" required pattern="[a-z0-9\-]+" defaultValue="image-classification-tight-slo" onChange={() => setDirty(true)} />
            <Select label="모델" placeholder="선택" value={picked} onChange={value => { setPicked(value); setDirty(true) }} options={[{ value: "resnet50-int8", label: "resnet50-int8", group: "분류" }, { value: "yolov8n", label: "yolov8n", group: "검출" }, { value: "llava-7b", label: "llava-7b", group: "질의응답" }]} />
            <Select multiple searchable label="결과" placeholder="결과 전체" value={outcomes} onChange={setOutcomes} options={[{ value: "ok", label: "성공" }, { value: "timeout", label: "시간 초과" }, { value: "rejected", label: "거절" }, { value: "error", label: "오류" }]} />
          </FormSection>
          <FormSection title="응답 목표" info="분위수와 지연 목표">
            <InputBlock label="분위수 (%)" type="number" defaultValue="99.5" onChange={() => setDirty(true)} />
            <div className="flex items-center gap-2"><Checkbox id="kit-dirty" checked={dirty} onCheckedChange={value => setDirty(value === true)} /><Label htmlFor="kit-dirty">변경 있음</Label></div>
          </FormSection>
        </div>
        <FormActions changes={dirty ? 2 : 0}>
          <Button type="button" variant="outline" onClick={() => toast({ title: "요청 실패", description: "HTTP 503", type: "error" })}>실패 알림</Button>
          <Button type="submit" variant="default" disabled={!dirty}>검토 후 적용</Button>
        </FormActions>
        <LeaveGuard when={dirty} />
      </form>

      <RawDialog open={raw} onOpenChange={setRaw} filename="image-classification-tight-slo" value={{ metadata: { name: "image-classification-tight-slo", uid: "5f0c2a9e-41b7-4d3e-9a8f-1c2b3d4e5f60", generation: 7 }, spec: { slo: { percentile: 99.5, targetLatencyNanos: 500000000n }, batchID: 18446744073709551615n } }} />
      <Dialog open={compare} onOpenChange={setCompare} title="판단 비교" size="lg" footer={<Button variant="outline" onClick={() => setCompare(false)}>닫기</Button>}>
        <Fields items={[{ label: "A 예상 전력", value: "214.6 W" }, { label: "B 예상 전력", value: "198.2 W" }]} />
      </Dialog>
      <ConfirmDialog
        open={confirm} onOpenChange={setConfirm} tone="destructive" title="서비스 삭제" target="image-classification-tight-slo"
        changes={[{ label: "접수", before: "허용", after: "중지" }, { label: "인스턴스", before: "3", after: "0" }, { label: "응답 목표 분위수", before: "image-classification-tight-slo-replica-set-7f9c4d", after: "p99.5 500 ms 이하, 대기 상한 64개" }]}
        reason={{ required: true }} confirmLabel="삭제"
        onConfirm={reason => { setConfirm(false); toast({ title: "삭제 요청됨", description: reason, type: "success" }) }}
      />
      <DetailDrawer open={drawer !== null} onOpenChange={open => { if (!open) setDrawer(null) }} title={drawer?.name ?? ""} status={drawer ? { label: toneLabel[drawer.tone], tone: drawer.tone } : undefined} actions={<Button size="sm" variant="outline">상세로</Button>}>
        {drawer && <Fields columns={1} items={[
          { label: "유입", value: formatRate(drawer.rate) }, { label: "지연", value: drawer.latency == null ? null : `${drawer.latency} ms`, reason: "응답 없음" },
          { label: "성공률", value: drawer.success == null ? null : formatPercent(drawer.success), reason: "요청 없음" }, { label: "배치", value: drawer.nodes.join(", ") },
        ]} />}
        {drawer && <DataTable label="배치" density="compact" pageSize={0} rows={drawer.nodes.map(name => ({ name }))} rowKey={node => node.name} columns={[
          { key: "node", header: "장치", cell: node => node.name }, { key: "mode", header: "전력 모드", cell: () => "MAXN" },
          { key: "watts", header: "전력", unit: "W", numeric: true, cell: () => "12.4" }, { key: "share", header: "분배", unit: "%", numeric: true, cell: () => null, missingReason: "분배 없음" },
        ]} />}
      </DetailDrawer>

      <PageTabs tabs={sectionTabs.scheduler} label="링크 탭 예" />
    </Page>
  )
}
