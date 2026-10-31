import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { createElement,type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule,ModuleKind,JsxEmit,ScriptTarget } from "typescript"
import * as refs from "./scheduler-history.ts"
import * as scheduler from "./scheduler.ts"
import * as kinds from "./records.ts"

const require=createRequire(import.meta.url)
function load(file:string,modules:Record<string,unknown>){const exports:Record<string,unknown>={};const compiled=transpileModule(readFileSync(new URL(file,import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText;new Function("require","exports",compiled)((name:string)=>name in modules?modules[name]:require(name),exports);return exports}

test("records renders real journal rows and exact server page scope without a hub or open selection",()=>{
  const params=new URLSearchParams("kind=judgments&from=2026-10-31T01%3A00%3A00.123456789Z&to=2026-10-31T02%3A00%3A00.987654321Z&cursor=81&limit=17&uid=service&filter=decision-action"),calls:string[]=[]
  const box=({children}:{children?:ReactNode})=>createElement("div",null,children)
  const row={at:"2026-10-31T01:30:00Z",offset:81,decision:{at:"2026-10-31T01:30:00Z",status:"Keep"}}
  const modules:Record<string,unknown>={react:require("react"),"next/navigation":{useSearchParams:()=>params,useRouter:()=>({replace:()=>{}}),usePathname:()=>"/records"},"next/link":{default:({children,href}:{children?:ReactNode;href:string})=>createElement("a",{href},children)},"@/lib/records":kinds,"@/lib/client":{useSession:()=>({config:{pageSize:25,maxPageSize:100,maxWindowHours:24}}),useChillQuery:(path:string,options:{enabled?:boolean})=>{if(options?.enabled===false)return {data:undefined};calls.push(path);return {data:{events:[row],nextCursor:"98"},isSuccess:true}}},"@/lib/read-types":{},"@/lib/scheduler":scheduler,"@/lib/scheduler-history":refs,"@/lib/date-time":{gapNotice:()=>"관측 공백",utcDateTime:(value:string)=>value},"@/lib/live-display":{displayTime:()=>"10:30",liveReason:(value:string)=>value},"@/features/scheduler":{judgmentLabels:{Keep:"현재 구성 유지"}},"@/features/scheduler-settings":{},"./scheduler-transitions":{},"@/lib/labels":{},"@/components/read-region":{QueryGate:box,SourceJSON:()=>null,SourceExport:()=>createElement("button",null,"내보내기")},"@/components/screen":{Screen:({children,title}:{children?:ReactNode;title:string})=>createElement("main",null,createElement("h1",null,title),children),Rows:box,RowDialog:()=>null,Empty:box},"@/components/ui/button":{Button:({children,disabled}:{children?:ReactNode;disabled?:boolean})=>createElement("button",{disabled},children)},"@/components/ui/input":{},"@/components/ui/sheet":{Sheet:({open,children}:{open:boolean;children?:ReactNode})=>open?box({children}):null,SheetContent:box,SheetHeader:box,SheetTitle:box}}
  Object.assign(modules["@/components/screen"] as object,{Row:()=>null})
  modules["@/components/records-navigation"]=load("../components/records-navigation.tsx",modules)
  const exports=load("../features/scheduler-history.tsx",modules)
  const html=renderToStaticMarkup(createElement(exports.SchedulerHistoryPage as ()=>ReactNode))
  assert.match(html,/<h1>기록<\/h1>/);assert.match(html,/판단·변경/);assert.match(html,/서비스 요청/);assert.match(html,/전력/);assert.match(html,/유지 ×1/);assert.match(html,/근거/);assert.doesNotMatch(html,/선택 기록 근거/);assert.doesNotMatch(html,/Invalid Date/)
  assert.equal(calls.length,1)
  const query=new URL(calls[0],"https://dashboard.invalid").searchParams
  for(const key of ["from","to","cursor","limit","uid","filter"])assert.equal(query.get(key),params.get(key))
  assert.equal(query.get("view"),"summary")
})
