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

### Page, PageHeader, PageTabs, Section, Rail
| Prop | Type | Notes |
|---|---|---|
| `PageHeader.title` | string | The page h1. Long titles wrap; actions stay on the right (below the title on phones). The title also labels the last breadcrumb when that segment is a name or an id. |
| `status` | `StatusInfo` | From `lib/status-labels`. |
| `badges` | node | Extra badges (e.g. admission). |
| `actions` | node | One primary `Button variant="default"`. |
| `more` | `MoreAction[]` = `{label,onSelect,destructive?,disabled?,disabledReason?}[]` | Overflow menu; destructive items last. With `disabledReason` a disabled item stays focusable and shows the reason. |
| `PageTabs.tabs` | `TabLink[]` | Link tabs (route change). Use `sectionTabs.scheduler` etc. |
| `PageTabs.param` + `defaultValue` + `tabs:{value,label,count?}[]` | | Query tabs (`?tab=`); the default value is not written to the URL. |
| `Section` | `title?, actions?, card?` | A titled region (h2); `card` puts it on a bordered surface. |
| `Rail` | `label, items:{value,label,count?,href?}[], value, onChange?, children?` | Group navigation: a vertical list beside `children` on desktops, a horizontal scroll strip above them on phones. Items with `href` are links, others buttons calling `onChange`; the current one has `aria-current`. Keep `value` in the URL. |

```tsx
<Page>
  <PageHeader title={name} status={serviceReadiness(view.readiness)} actions={<Button variant="default">수정</Button>}
    more={[{ label: "원본 보기", onSelect: () => setRaw(true) }, { label: "삭제", destructive: true, onSelect: () => setDel(true) }]} />
  <PageTabs param="tab" defaultValue="overview" tabs={[{ value: "overview", label: "현황" }, { value: "requests", label: "요청" }]} />
  <Section title="배치" card>…</Section>
</Page>
```
Section list pages show `<PageTabs tabs={sectionTabs.scheduler} />` under the header. Breadcrumbs are automatic (top bar, detail/new/edit pages only): route words get their label, and the last crumb shows the `PageHeader` title when it is a name, an id, "새로 만들기" or "수정" (an id with no header shows "상세").

```tsx
<Rail label="설정 묶음" value={group} onChange={setGroup} items={[{ value: "targets", label: "대상", count: 2 }, { value: "limits", label: "한도" }]}>
  <SettingsGroup group={group} />
</Rail>
```

### StatTile, StatRow, MeterBar
| Prop | Type | Notes |
|---|---|---|
| `label` | string | |
| `value` | node or null | null shows "—" with `reason`. Already formatted number, no unit. |
| `unit` | string | e.g. `"W"`. |
| `sub` | node | One secondary value. |
| `info` | string | One-sentence definition tooltip. |
| `status` | `StatusInfo` | Small marker in the label row. |
| `href` | string | Whole tile links. |
| `StatRow.columns` | `2…6` | Tiles across on desktops (default 4); always two across on tablets and phones, where an odd last tile spans the row. |
| `href` layout | | The label is the link and its hit area covers the tile; `info` and `status` stay separate controls above it (never nest controls in a link). |

```tsx
<StatRow columns={3}><StatTile label="전력" value={formatNumber(w, 1)} unit="W" sub={`전체 풀 ${formatWatts(total)}`} href="/devices" /></StatRow>
```
`MeterBar value max label tone?="progress" marker? reason? width?="cell"|"full"`: thin bar of `value` out of `max` (a `role="meter"`), in a cell (64 px) or across a tile. `marker` draws a tick (a limit). Display only: the tone never states a verdict. Absent value: "—" with `reason`.
```tsx
cell: row => <span className="inline-flex items-center gap-2"><MeterBar value={row.watts} max={scale} label={`전력 ${row.watts} W`} />{formatNumber(row.watts, 1)}</span>
```

### StatusBadge, StatusDot, Steps
Five tones: `ok`, `warn`, `fail`, `progress` (primary), `idle`. Shape (icon) + colour + text.
| Prop | Type | Notes |
|---|---|---|
| `status` | `StatusInfo` | Preferred: `serviceReadiness(code)`, `actionStatus(code)`, … Unknown code: "—" plus tooltip `코드 X`. |
| `tone`, `label` | | Direct form. |
| `reason` | node | Badge tooltip. |
| `StatusDot.showLabel` | boolean | Icon only (label in tooltip) or icon + text. A `tone` with no `label` and no `status` is decoration and hidden from screen readers. |

Vocabulary in `lib/status-labels.ts`: `serviceReadiness, admissionStatus(bool), autonomyState, autonomyEnabled(bool), actionStatus, measurementStatus, measurementPhaseStatus, evaluationPhase, experimentVerdict, experimentStepStatus, energyStatus, sloVerdict(met), decisionStatus, candidateStatus, schedulerReason, evaluationReason, evaluationPool/Verdict/Execution/Cleanup, loadJobStatus, injectionOutcome`. Add new codes there, not in screens; reuse the existing word for the same concept ("측정 결과 없음", "취소", "확인 후 반환").

`Steps label items={[{label, status:"done"|"current"|"failed"|"pending", reason?}]}`: ordered stages in one row that wraps on phones; icon + text per step, `aria-current="step"` on the current one, `reason` as a tooltip (e.g. why a step failed).
```tsx
<Steps label="진행" items={[{ label: "준비", status: "done" }, { label: "측정", status: "failed", reason: "부하 생성 품질 미달" }, { label: "정리", status: "pending" }]} />
```

### HeadroomBar
`observed`, `target` (same unit), `unit?`, `reason?`. Colour by observed/target: < 0.7 primary, 0.7 to 1.0 warn, > 1.0 fail; the tick marks the target. Display only; the verdict word comes from the server (`metric.slo.met`).
```tsx
<HeadroomBar observed={slo.quantileMs} target={slo.targetMs} />
```

### DataTable
TanStack table state, Avara table markup. Rows are 40 px, one line per cell; cut text (cells and headers) shows a tooltip. Below a 30rem container (phones, narrow columns) rows stack into label/value lines by default (`narrow`); a table that still scrolls sideways shows a shadow on the edge with more columns.
| Prop | Type | Notes |
|---|---|---|
| `label` | string | Accessible name (caption, scroll region). Required. |
| `rows`, `rowKey` | `T[]`, `(row)=>string` | Stable key; rows keep their order under live updates unless the user sorts. |
| `columns` | `Column<T>[]` | `{key, header, unit?, numeric?, cell, sortValue?, csv?, missingReason?, missing?, width?}`. `cell` returning null shows "—"; `missing(row)` gives that row's reason (else `missingReason`). |
| `rowHref` | `(row)=>string` | Row navigates; first cell becomes the link. |
| `onRowClick`, `selectedKey` | | Opens a `DetailDrawer`; row is keyboard-activatable. |
| `rowActions` | `(row)=>MoreAction[]` | Menu button in a narrow last column (not one of the seven), named "동작 <first cell>". Clicks in it never open the row. |
| `defaultSort` / `sort`+`onSortChange` | `{key,desc}` | Client sort / controlled (server) sort. |
| `pageSize` | number | Client paging (default 25; 0 = all). The page survives live updates; it is clamped when rows shrink and reset by a sort. |
| `paging` | `{mode:"offset",offset,limit,total?,onOffsetChange}` or `{mode:"cursor",hasPrevious,hasNext,onPrevious,onNext}` | Server paging; rows are one page. |
| `download` | `{filename}` | "내려받기": CSV of loaded rows (`csv` or `sortValue` per column). |
| `title`, `toolbar`, `actions` | node | Heading row (title is an h2, Section size); put a `FilterBar` in `toolbar`. |
| `empty`, `busy` | | Empty node; dims rows while refetching. |
| `density` | `"default"|"compact"` | `compact`: tighter cells, no minimum width, no pinned column, `narrow="scroll"`; use it in dialogs and drawers. |
| `minWidth` | number (px) | Width below which the table scrolls sideways (default 104 px per column, 0 with `compact`). |
| `narrow` | `"stack"|"scroll"` | Below a 30rem container. `stack` (default): each row is a block, the first cell its title, the others "header (unit) value" lines; sortable headers stay as a row of sort buttons. `scroll`: columns kept, first column pinned, edge shadow. Use `scroll` only when columns must be compared across rows (a matrix-like table). |
| `stickyFirst` | boolean | Pin the first column at every width (default: phones with `narrow="scroll"`). |

```tsx
<DataTable label="서비스" rows={rows} rowKey={r => r.uid} rowHref={r => `/services/${encodeURIComponent(r.name)}?uid=${r.uid}`}
  defaultSort={{ key: "name", desc: false }} download={{ filename: "services" }}
  columns={[{ key: "name", header: "서비스", cell: r => r.name, sortValue: r => r.name },
            { key: "rate", header: "유입", unit: "req/s", numeric: true, cell: r => r.rate?.toFixed(1), sortValue: r => r.rate }]} />
```

### Matrix, MatrixCell
Rows by columns (e.g. power modes by execution paths) with a pinned row-header column.
`Matrix label rowHeader columns={[{key,label,hint?}]} groups={[{key,label?,rows:[{key,label,cells:{[columnKey]: node}}]}]} legend? empty? missingReason?`; absent cells show "—". A pressable cell is `<MatrixCell label="측정 있음 배치 3/4" onClick={…} dim?>…</MatrixCell>`.

### QueryBoundary, EmptyState, NotReady
`QueryBoundary query={useChillQuery(...)}` renders: skeleton (`skeleton="table"|"tiles"|"block"` or a node), "초기화 중" (CoverageWarming), "권한 없음" / "접속 정보 확인 필요" (403/401), "조회 실패" with code and "다시 시도", `empty` when `isEmpty(data)`, else `children(data)`.
```tsx
<QueryBoundary query={workloads} isEmpty={d => d.length === 0} empty={<EmptyState title="서비스 없음" action={<Button>서비스 등록</Button>} />}>
  {data => <DataTable … rows={data} />}
</QueryBoundary>
```
`EmptyState`: `title` (short label), `action?`, `icon?`, `compact?`.

### DetailDrawer, Dialog
`DetailDrawer open, onOpenChange, title, status?, actions?, footer?, children`. Native modal dialog from the right; Esc and backdrop close it; focus returns to the row.

`Dialog open, onOpenChange, title, size?="sm"|"md"|"lg"|"xl", footer?, children`: the same native modal for short content that is neither a confirmation nor a row preview (a comparison table). Esc closes it; focus returns to the opener, also when it was opened from a `more` menu item. Never import `DialogBlock` in screens.

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

### IdText, TimeText, Fields, Feed, Hint, Missing
- `IdText value head? tail? copy?`: `5f0c2a9e…5f60` + full value tooltip + copy.
- `TimeText at mode?="relative"|"absolute" seconds?`: relative ("방금" within 5 s either side, "3분 전", 1 s clock) with KST tooltip, or absolute KST.
- `Fields items={[{label, value, reason?, info?}]} columns?={1|2|3}`: label/value list; a cut value shows a tooltip.
- `Feed items={[{key, title, status?|tone?, secondary?, at?, meta?, href?}]} empty? label?`: one-line rows (status mark, title, secondary text, relative time on the right); rows with `href` link. For side columns ("최근 변경", "진행 중").
- `Hint text focusable?`: tooltip wrapper; with `focusable` the hint is also the wrapper's description for screen readers. `InfoHint text`: the "i" button (24 px) for a one-sentence definition; `info` props use it. `Missing reason`: the "—" itself. `useNow()`: shared 1 s clock.

### TimeSeries, Sparkline
`TimeSeries data={[{t, a: number|null}]} series={[{key:"a", label:"전체 풀", color?:1-6}]} label unit? height? domain?=[from,to] reference?={value,label} digits? legend? x?="time"|"seconds" curve?="linear"|"step" summary?`.
- Null samples are gaps (never connected); a sample with gaps on both sides is drawn as a dot. No animation.
- Y axis: round ticks from zero with the decimals the range needs (0.25 steps stay apart); the reference line is always inside it.
- X axis: KST clock ticks that never repeat a label (seconds under one-minute steps, the date beyond a day). `x="seconds"`: `t` is seconds from the start, labelled "1분 30초" (load scenarios); `curve="step"` holds each value until the next sample.
- `legend` lists the series under the chart. Screen readers get `label` plus `summary` (default: each series' last value and its time); the drawing itself is hidden from them.
```tsx
<TimeSeries label="서비스별 요청률" data={steps} x="seconds" curve="step" legend unit="req/s" series={names.map((name, i) => ({ key: `s${i}`, label: name }))} />
```
`Sparkline values={(number|null)[]} label width? height?` for cells.

### FormSection, FormActions, LeaveGuard
- `FormSection title info?`: fieldset card. Inputs: Avara `InputBlock` (`@/components/custom/blocks/InputBlock`) and `Select` from the kit (same as `SelectBlock`) with `label`; shadcn `Checkbox`, `Switch`, `RadioGroup`, `Textarea`, `Label` from `@/components/ui/...`.
- `FormActions changes?`: sticky bottom row, "변경 n건", buttons on the right ("검토 후 적용").
- `<LeaveGuard when={dirty} />`: confirms before reload/close and in-app link navigation.
- Validation: keep native `required`, `min`, `max`, `pattern` on the controls. Kit inputs (`InputBlock`, `Select`, the `ConfirmDialog` reason) never show the browser's bubble: a failed constraint shows a Korean message under the field ("입력 필요", "선택 필요", "최소 1"…) with `aria-invalid` and `aria-describedby`, on submit (focus moves to the first invalid field) or when leaving an invalid field. Any other control's bubble says the same Korean text. Pass `error` for server or cross-field errors. A `pattern` is a `v`-flag regex: escape `-` in a class (`[a-z0-9\-]+`).

### Select
`Select` (= `SelectBlock`) `label? aria-label? options={[{value,label,group?}]} value onChange placeholder? helperText? error? icon? disabled? inputSize? multiple? searchable?`.
- Single choice is a styled native `<select>`: keyboard, type-ahead, platform picker on phones, works in dialogs and drawers. Options with `group` sit under an `<optgroup>`. A value not in `options` shows the placeholder.
- `multiple` (`value: string[]`): a button opening a popover of checkboxes (Esc and outside click close it, focus returns to the button); `searchable` adds a filter box.
- Name: `label` (visible), else `aria-label`, else the `icon` prefix text. `error` is announced with the control.

### FilterBar, TimeRange
- `FilterBar search?={value,onChange,placeholder?} segments?={label,value,onChange,options:{value,label,count?}[], multiple?}[] selects?={label,value,onChange,options}[] end?`.
- A segment with `multiple: true` takes `value: string[]` (several chips pressed at once; `[]` = no filter). Selects show their `label` as a visible prefix and use it as the accessible name.
- Keep filters in the URL. A multiple segment is one comma list (`?status=fail,warn`):
```tsx
const search = useSearchParams(), router = useRouter(), pathname = usePathname()
const status = search.get("status")?.split(",").filter(Boolean) ?? []
const setStatus = (next: string[]) => {
  const params = new URLSearchParams(search.toString())
  if (next.length) params.set("status", next.join(",")); else params.delete("status")
  router.replace(`${pathname}?${params}`, { scroll: false })
}
<FilterBar segments={[{ label: "상태", multiple: true, value: status, onChange: setStatus, options: [{ value: "fail", label: "실패" }, { value: "warn", label: "주의" }] }]} />
```
- `TimeRange value onChange presets={["15m","1h"]} custom?={maxSpanMs}`; store it with `const [range, setRange] = useTimeRangeParam(["15m","1h"], "15m", 3_600_000)` and resolve with `resolveTimeRange(range, now)`.

### Button, useToast
`Button variant="default"|"outline"|"secondary"|"destructive"|"ghost"|"link" size="default"|"sm"|"lg"|"icon" loading iconLeft iconRight disabledReason?`; `buttonClassName({variant,size})` styles a `Link`. With `disabled` and `disabledReason` the button stays focusable (`aria-disabled`, clicks ignored) and shows the reason as a tooltip on hover and keyboard focus.

`const { toast } = useToast(); toast({ title, description?, type: "success"|"error"|"warning"|"info", duration? })`. Toasts sit bottom right above a sticky `FormActions` bar and close themselves (5 s, warnings 8 s, errors 10 s; `duration: 0` keeps one until closed); hover or focus pauses the timer; every toast has a close button. Errors are announced assertively, others politely. Use a toast for a result; keep anything the user must act on on the page.

## Other Avara blocks
Installed under `components/custom/blocks/` and usable inside screens when the kit has no fit: `Stepper`, `TimelineBlock`, `Accordion`, `Collapsible`, `ChartBlock` (bar/donut), `ProgressBlock`, `ScrollAreaBlock`, `range-date-picker`, `VerticalTabs`. Read the file before use; keep their theme-token props (no colour presets).
