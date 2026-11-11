import assert from "node:assert/strict"
import { test } from "node:test"
import { createRequire } from "node:module"
import { readFileSync } from "node:fs"
import { createElement, type ReactNode, type ComponentType } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { transpileModule, ModuleKind, JsxEmit, ScriptTarget } from "typescript"

import * as React from "react"
import * as summary from "./measurement-summary.ts"
test("access form exposes token storage and expiry as facts without changing authentication",()=>{
  const require=createRequire(import.meta.url),exports:Record<string,ComponentType>={}
  const wrap=({children}:{children?:ReactNode})=>createElement("div",{},children)
  let stateIndex=0
  const modules:Record<string,unknown>={
    "next/navigation":{useRouter:()=>({replace:()=>{}})},
    "react":{...React,useState:(initial:unknown)=>React.useState(stateIndex++===8?true:initial)},
    "@/components/ui/button":{Button:wrap},"@/components/ui/input":{Input:()=>createElement("input",{type:"password"})},
    "@/components/ui/dialog":{Dialog:wrap,DialogContent:wrap,DialogDescription:wrap,DialogHeader:wrap,DialogTitle:wrap,DialogTrigger:wrap},
    "@/components/brand":{BrandMark:()=>null,FinField:()=>null},"lucide-react":{KeyRound:()=>null},
    "@/lib/scheduler":{},"@/lib/session-token":{},
  }
  const transport={}
  const transportSource=readFileSync(new URL("./client-transport.ts",import.meta.url),"utf8")
  new Function("require","exports",transpileModule(transportSource,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS}}).outputText)((name:string)=>name==="@/lib/measurement-summary"?summary:require(name),transport)
  modules["@/lib/client-transport"]=transport
  const source=readFileSync(new URL("./client.tsx",import.meta.url),"utf8")
  const compiled=transpileModule(source,{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS,jsx:JsxEmit.ReactJSX}}).outputText
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  const html=renderToStaticMarkup(createElement(exports.SessionProvider,{},createElement("span",{},"contents")))
  assert.match(html,/<table[^>]*aria-label="토큰 보관·유효 범위"/)
  for(const label of ["보관 위치","새로고침","탭 복제·브라우저 복원","이 탭 삭제 영향","토큰 유효기간","미제공"])assert.ok(html.includes(label))
  assert.doesNotMatch(html,/토큰은 이 시스템의 이 탭에 저장되어/)
})
