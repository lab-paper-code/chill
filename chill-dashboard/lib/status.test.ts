import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement, type ComponentType, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"
import { ABSENCE_LABELS, STATUS_AXES, commonStatus, normalizeStatusLabel, type StatusValue } from "./status.ts"
import * as status from "./status.ts"
import * as labels from "./labels.ts"
import * as scheduler from "./scheduler.ts"
import type { DataColumn } from "../components/data-table.tsx"

const shared:StatusValue={axis:STATUS_AXES.quality,label:"판정 불가",tone:"idle",reason:"기준 미제공"}

test("absence synonyms keep permission, failure, stale observation and unknown outcomes distinct",()=>{
  for(const label of ABSENCE_LABELS)assert.equal(normalizeStatusLabel(label),label)
  for(const [input,expected] of [["기록 없음","미기록"],["제공 없음","미제공"],["확인 불가","미확인"],["계산값 없음","미제공"],["사유 미제공","미제공"],["미지원","지원 안 함"],["결과 불명","결과 미확인"],["관측 지연","오래된 관측"],["Forbidden","권한 없음"],["MeasurementUnavailable","조회 실패"],["Unknown","미확인"],["Completed","완료"]])assert.equal(normalizeStatusLabel(input),expected)
  assert.equal(normalizeStatusLabel(null),"미확인")
  assert.equal(normalizeStatusLabel(undefined),"미확인")
  assert.equal(normalizeStatusLabel("  "),"미확인")
  assert.equal(normalizeStatusLabel("0"),"0")
  assert.equal(normalizeStatusLabel("항목 없음"),"항목 없음")
  assert.equal(normalizeStatusLabel("미측정"),"미측정")
})

test("header compression requires a nonempty scope with the same axis, state, tone and reason",()=>{
  assert.deepEqual(commonStatus([shared,{...shared}]),shared)
  assert.equal(commonStatus([]),undefined)
  for(const changed of [{...shared,reason:"표본 부족"},{...shared,tone:"warn" as const},{...shared,axis:STATUS_AXES.performance},{...shared,label:"기준 충족"}])assert.equal(commonStatus([shared,changed]),undefined)
  assert.equal(commonStatus([{...shared,label:"미지원"},{...shared,label:"지원 안 함"}])?.label,"지원 안 함")
})

const require=createRequire(import.meta.url)
function load(path:string,modules:Record<string,unknown>) {
  const compiled=transpileModule(readFileSync(new URL(path,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  const exports:Record<string,ComponentType<Record<string,unknown>>>={}
  new Function("require","exports",compiled)((name:string)=>modules[name]??(name.startsWith("@/")?{}:require(name)),exports)
  return exports
}
const container=(tag:string)=>function TestContainer({children,...props}:{children?:ReactNode}) {return createElement(tag,props,children)}
const icons=Object.fromEntries(["Braces","CircleCheck","CircleDashed","CircleX","Download","Info","RefreshCw","TriangleAlert","ArrowDown","ArrowUp","ArrowUpDown","ChevronLeft","ChevronRight","Search"].map(name=>[name,(props:Record<string,unknown>)=>createElement("svg",{...props,"data-icon":name})]))
const readRegion=load("../components/read-region.tsx",{"@/lib/labels":labels,"@/lib/scheduler":scheduler,"lucide-react":icons})
const statusCell=load("../components/status-cell.tsx",{"@/components/read-region":readRegion,"@/lib/status":status,"@/components/ui/tooltip":{Tooltip:container("span"),TooltipTrigger:({children}:{children:ReactNode})=>children,TooltipContent:container("span")}})

test("status cells expose their axis, a non-color marker and the short reason",()=>{
  const html=renderToStaticMarkup(createElement(statusCell.StatusCell,shared))
  assert.match(html,/품질 판정/)
  assert.match(html,/판정 불가/)
  assert.match(html,/기준 미제공/)
  assert.match(html,/data-icon="CircleDashed"/)
  assert.match(html,/data-tone="idle"/)
  const long="비교할 기준이 제공되지 않아 현재 표본으로 품질을 판정할 수 없습니다. ".repeat(3)
  const expanded=renderToStaticMarkup(createElement(statusCell.StatusCell,{...shared,reason:long}))
  assert.ok(expanded.includes(long))
  assert.match(expanded,/tabindex="0"/)
  assert.doesNotMatch(expanded,/\bw-max\b/)
})

test("data tables retain required columns and put units and the common status in the header",()=>{
  const table=load("../components/data-table.tsx",{
    "@/lib/scheduler":scheduler,"@/components/status-cell":statusCell,"@/components/read-region":{DetailsDialog:()=>null},"lucide-react":icons,
    "@/components/ui/table":{Table:container("table"),TableBody:container("tbody"),TableCell:container("td"),TableHead:container("th"),TableHeader:container("thead"),TableRow:container("tr")},
    "@/components/ui/button":{Button:container("button")},
  })
  const columns:DataColumn<{name:string;value:number}>[]=[{key:"name",label:"서비스",value:row=>row.name},{key:"latency",label:"지연",unit:"ms",numeric:true,value:row=>row.value},{key:"quality",label:"품질",headerStatus:commonStatus([shared,shared]),value:()=>"판정 불가"}]
  const html=renderToStaticMarkup(createElement(table.DataTable,{data:[{name:"long-service-name".repeat(10),value:0}],columns,searchable:false}))
  assert.match(html,/지연 \(ms\)/)
  const numericCell=html.match(/<td\b[^>]*data-label="지연 \(ms\)"[^>]*>[\s\S]*?<\/td>/)?.[0]
  assert.ok(numericCell)
  assert.match(numericCell,/text-right tabular-nums/)
  assert.match(numericCell,/>0<\/div><\/td>/)
  assert.equal((html.match(/<th\b/g)??[]).length,3)
  assert.equal((html.match(/<td\b/g)??[]).length,3)
  assert.match(html,/품질 판정/)
  assert.match(html,/overflow-wrap:anywhere/)
})
