# Kit guide

Screens are assembled only from `@/components/kit` (plus `lib/` data helpers). Live gallery: `/kit` (development only).

## Screen file convention

- Routes live in `app/(app)/...`; each `page.tsx` is a thin server component that renders one client screen. Screen owners never edit `app/` or `components/`.
- A screen is `screens/<area>/<screen>.tsx`, starts with `"use client"`, and exports the component the page already imports (for example `ServiceDetailScreen({ name })`). Areas: `overview, services, devices, scheduler, measurements, experiments, catalog`.
- Props are decoded route params (`name`, `id`, `kind: CatalogKind`). Read `?uid=`, `?tab=` and filters with `useSearchParams()`; keep selection, tab, filters and time range in the URL.
- Area-private helpers go in `lib/<area>-*.ts` (pure) next to a `*.test.ts`.
- Data: `useChillQuery`, `useChillQueries`, `useChillMutation`, `useSession` from `@/lib/client`; `useLive()` from `@/lib/use-live` (`state` is frozen while the user pauses; never read the socket yourself).
- Labels for server codes: `@/lib/status-labels`. Units: `@/lib/format`. Routes and tab rows: `@/lib/routes`.

## Screen checklist (design section 2)

1. One question per screen: summary row, one main region, at most one side region.
2. No sentences on screen. Labels, values, units. A definition goes in an `info` tooltip, one sentence.
3. No raw JSON, UID or digest in the layout. Raw data only via `RawDialog` from the header `more` menu; identifiers via `IdText`.
4. Tables: at most 7 columns, one value per cell, one line. The rest goes to the detail page or `DetailDrawer`.
5. List to detail is a page change. Detail pages use `PageTabs` with `param="tab"`. A drawer previews a row; dialogs only confirm or take a short input.
6. One primary action in `PageHeader.actions`. Delete and other risky actions go in `more` with `destructive: true` and a `ConfirmDialog`.
7. Absent value: return `null` from the cell or `value`, the kit shows "—"; put the reason in `missingReason`/`reason`. Errors and no-permission are region states via `QueryBoundary`.
8. Times are KST. Live screens: `<TimeText at=… />`. Record tables: `<TimeText mode="absolute" />`.
9. Numbers: `numeric: true` columns (right-aligned, tabular). Units in the header (`unit`), never in the cell.
10. Verdicts and statistics come from the server. Allowed in the browser: unit conversion, sums and counts, ratio or difference of two server values, comparing a value with its server target. Not allowed: quantiles, power integration, gap interpolation, inventing a verdict rule.
11. Changes: show target and before/after (`ConfirmDialog.changes`), send `version` (If-Match) where required, never auto-retry. `error.unknownOutcome` means "결과 미확인". Result goes to a toast. Forms use `LeaveGuard`.
12. Server codes never appear raw: map them in `lib/status-labels.ts`; unknown codes render "—" with the code in the tooltip.
13. No colours, spacing or table styling in screen files: use kit props. No entrance or count-up animation on live values; no reordering of rows when values change (sort by a stable key).
14. Works at 1440 px and 390 px, light and dark, by keyboard.

## Components

All imports: `import { … } from "@/components/kit"`.

### Page, PageHeader, PageTabs, Section
| Prop | Type | Notes |
|---|---|---|
| `PageHeader.title` | string | The page h1. |
| `status` | `StatusInfo` | From `lib/status-labels`. |
| `badges` | node | Extra badges (e.g. admission). |
| `actions` | node | One primary `Button variant="default"`. |
| `more` | `{label,onSelect,destructive?,disabled?}[]` | Overflow menu; destructive items last. |
| `PageTabs.tabs` | `TabLink[]` | Link tabs (route change). Use `sectionTabs.scheduler` etc. |
| `PageTabs.param` + `defaultValue` + `tabs:{value,label,count?}[]` | | Query tabs (`?tab=`); the default value is not written to the URL. |
| `Section` | `title?, actions?, card?` | A titled region (h2); `card` puts it on a bordered surface. |

```tsx
<Page>
  <PageHeader title={name} status={serviceReadiness(view.readiness)} actions={<Button variant="default">수정</Button>}
    more={[{ label: "원본 보기", onSelect: () => setRaw(true) }, { label: "삭제", destructive: true, onSelect: () => setDel(true) }]} />
  <PageTabs param="tab" defaultValue="overview" tabs={[{ value: "overview", label: "현황" }, { value: "requests", label: "요청" }]} />
  <Section title="배치" card>…</Section>
</Page>
```
Section list pages show `<PageTabs tabs={sectionTabs.scheduler} />` under the header. Breadcrumbs are automatic (top bar, detail/new/edit pages only).

### StatTile, StatRow
| Prop | Type | Notes |
|---|---|---|
| `label` | string | |
| `value` | node or null | null shows "—" with `reason`. Already formatted number, no unit. |
| `unit` | string | e.g. `"W"`. |
| `sub` | node | One secondary value. |
| `info` | string | One-sentence definition tooltip. |
| `status` | `StatusInfo` | Small marker in the label row. |
| `href` | string | Whole tile links. |

```tsx
<StatRow><StatTile label="전력" value={formatNumber(w, 1)} unit="W" sub={`전체 풀 ${formatWatts(total)}`} href="/devices" /></StatRow>
```

### StatusBadge, StatusDot
Five tones: `ok`, `warn`, `fail`, `progress` (primary), `idle`. Shape (icon) + colour + text.
| Prop | Type | Notes |
|---|---|---|
| `status` | `StatusInfo` | Preferred: `serviceReadiness(code)`, `actionStatus(code)`, … Unknown code: "—" plus tooltip `코드 X`. |
| `tone`, `label` | | Direct form. |
| `reason` | node | Badge tooltip. |
| `StatusDot.showLabel` | boolean | Icon only (label in tooltip) or icon + text. |

Vocabulary in `lib/status-labels.ts`: `serviceReadiness, admissionStatus(bool), autonomyState, autonomyEnabled(bool), actionStatus, measurementStatus, measurementPhaseStatus, evaluationPhase, experimentVerdict, sloVerdict(met)`. Add new codes there, not in screens.

### HeadroomBar
`observed`, `target` (same unit), `unit?`, `reason?`. Colour by observed/target: < 0.7 primary, 0.7 to 1.0 warn, > 1.0 fail; the tick marks the target. Display only; the verdict word comes from the server (`metric.slo.met`).
```tsx
<HeadroomBar observed={slo.quantileMs} target={slo.targetMs} />
```

### DataTable
TanStack table state, Avara table markup. Rows are 40 px, one line per cell; cut text shows a tooltip.
| Prop | Type | Notes |
|---|---|---|
| `label` | string | Accessible name (caption, scroll region). Required. |
| `rows`, `rowKey` | `T[]`, `(row)=>string` | Stable key; rows keep their order under live updates unless the user sorts. |
| `columns` | `Column<T>[]` | `{key, header, unit?, numeric?, cell, sortValue?, csv?, missingReason?, width?}`. `cell` returning null shows "—". |
| `rowHref` | `(row)=>string` | Row navigates; first cell becomes the link. |
| `onRowClick`, `selectedKey` | | Opens a `DetailDrawer`; row is keyboard-activatable. |
| `defaultSort` / `sort`+`onSortChange` | `{key,desc}` | Client sort / controlled (server) sort. |
| `pageSize` | number | Client paging (default 25; 0 = all). |
| `paging` | `{mode:"offset",offset,limit,total?,onOffsetChange}` or `{mode:"cursor",hasPrevious,hasNext,onPrevious,onNext}` | Server paging; rows are one page. |
| `download` | `{filename}` | "내려받기": CSV of loaded rows (`csv` or `sortValue` per column). |
| `title`, `toolbar`, `actions` | node | Heading row; put a `FilterBar` in `toolbar`. |
| `empty`, `busy` | | Empty node; dims rows while refetching. |

```tsx
<DataTable label="서비스" rows={rows} rowKey={r => r.uid} rowHref={r => `/services/${encodeURIComponent(r.name)}?uid=${r.uid}`}
  defaultSort={{ key: "name", desc: false }} download={{ filename: "services" }}
  columns={[{ key: "name", header: "서비스", cell: r => r.name, sortValue: r => r.name },
            { key: "rate", header: "유입", unit: "req/s", numeric: true, cell: r => r.rate?.toFixed(1), sortValue: r => r.rate }]} />
```

### QueryBoundary, EmptyState, NotReady
`QueryBoundary query={useChillQuery(...)}` renders: skeleton (`skeleton="table"|"tiles"|"block"` or a node), "초기화 중" (CoverageWarming), "권한 없음" / "접속 정보 확인 필요" (403/401), "조회 실패" with code and "다시 시도", `empty` when `isEmpty(data)`, else `children(data)`.
```tsx
<QueryBoundary query={workloads} isEmpty={d => d.length === 0} empty={<EmptyState title="서비스 없음" action={<Button>서비스 등록</Button>} />}>
  {data => <DataTable … rows={data} />}
</QueryBoundary>
```
`EmptyState`: `title` (short label), `action?`, `icon?`, `compact?`.

### DetailDrawer
`open, onOpenChange, title, status?, actions?, footer?, children`. Native modal dialog from the right; Esc and backdrop close it; focus returns to the row.

### ConfirmDialog
| Prop | Type | Notes |
|---|---|---|
| `open`, `onOpenChange`, `title` | | |
| `target` | node | What changes. |
| `changes` | `{label,before,after}[]` | Before/after table. |
| `children` | node | Short impact. |
| `tone` | `"default"|"destructive"` | Destructive: red button, focus on 취소. |
| `reason` | `{label?,required?,maxLength?}` | Reason textarea; `required` blocks submit. |
| `pending` | boolean | Locks the dialog while the mutation runs. |
| `onConfirm` | `(reason)=>void` | Run the mutation; close it yourself on success. |

```tsx
<ConfirmDialog open={open} onOpenChange={setOpen} title="자동 제어 끄기" target={name} changes={[{ label: "자동 제어", before: "켜짐", after: "꺼짐" }]}
  reason={{ required: true }} pending={m.isPending} onConfirm={reason => m.mutate({ … }, { onSuccess: () => { setOpen(false); toast({ title: "적용됨", type: "success" }) } })} />
```

### RawDialog
`open, onOpenChange, value, filename, title?`. Pretty JSON through `stringifySchedulerJSON` (exact 64-bit integers from `parseSchedulerJSON`), copy and download.

### IdText, TimeText, Fields, Hint, Missing
- `IdText value head? tail? copy?`: `5f0c2a9e…5f60` + full value tooltip + copy.
- `TimeText at mode?="relative"|"absolute" seconds?`: relative ("3분 전", 1 s clock) with KST tooltip, or absolute KST.
- `Fields items={[{label, value, reason?, info?}]} columns?={1|2|3}`: label/value list.
- `Hint text`: tooltip wrapper. `Missing reason`: the "—" itself. `useNow()`: shared 1 s clock.

### TimeSeries, Sparkline
`TimeSeries data={[{t, a: number|null}]} series={[{key:"a", label:"전체 풀", color?:1-6}]} unit? height? domain?=[from,to] reference?={value,label} label`. Null samples are gaps (never connected), no animation, KST axis. `Sparkline values={(number|null)[]} label width? height?` for cells.

### FormSection, FormActions, LeaveGuard
- `FormSection title info?`: fieldset card. Inputs: Avara `InputBlock`/`SelectBlock` (`@/components/custom/blocks/...`) with `label`; shadcn `Checkbox`, `Switch`, `RadioGroup`, `Textarea`, `Label` from `@/components/ui/...`.
- `FormActions changes?`: sticky bottom row, "변경 n건", buttons on the right ("검토 후 적용").
- `<LeaveGuard when={dirty} />`: confirms before reload/close and in-app link navigation.

### FilterBar, TimeRange
- `FilterBar search?={value,onChange,placeholder?} segments?={label,value,onChange,options:{value,label,count?}[]}[] selects?={label,value,onChange,options}[] end?`.
- `TimeRange value onChange presets={["15m","1h"]} custom?={maxSpanMs}`; store it with `const [range, setRange] = useTimeRangeParam(["15m","1h"], "15m", 3_600_000)` and resolve with `resolveTimeRange(range, now)`.

### Button, useToast
`Button variant="default"|"outline"|"secondary"|"destructive"|"ghost"|"link" size="default"|"sm"|"lg"|"icon" loading iconLeft iconRight`; `buttonClassName({variant,size})` styles a `Link`. `const { toast } = useToast(); toast({ title, description?, type: "success"|"error"|"warning"|"info" })`.

## Other Avara blocks
Installed under `components/custom/blocks/` and usable inside screens when the kit has no fit: `Stepper`, `TimelineBlock`, `Accordion`, `Collapsible`, `ChartBlock` (bar/donut), `ProgressBlock`, `ScrollAreaBlock`, `range-date-picker`, `VerticalTabs`. Read the file before use; keep their theme-token props (no colour presets).
