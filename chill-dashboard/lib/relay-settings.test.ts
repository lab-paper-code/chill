import assert from "node:assert/strict"
import test from "node:test"
import { EventEmitter } from "node:events"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { transpileModule, ModuleKind, ScriptTarget } from "typescript"
import { serverConfig } from "./server-config.ts"
import type { relay } from "./relay.ts"

const require=createRequire(import.meta.url)
test("settings relay preserves ETag and If-Match verbatim without widening other response headers",async()=>{
  const sent:{headers:Record<string,string>;body:string}[]=[]
  let tag:string|undefined='"8"',status=200
  const config=serverConfig({CHILL_BASE_URL:"https://chill.test",CHILL_NAMESPACE:"chill",CHILL_SYSTEM_NAME:"chill"})
  const modules:Record<string,unknown>={"./server-config.ts":{serverConfig:()=>config},"node:https":{request:(_url:unknown,options:{headers:Record<string,string>},receive:(response:EventEmitter)=>void)=>Object.assign(new EventEmitter(),{end(body:Buffer){
    sent.push({headers:options.headers,body:body.toString()})
    const response=Object.assign(new EventEmitter(),{statusCode:status,headers:{"content-type":"application/json",...(tag?{etag:tag}:{})}})
    receive(response)
    queueMicrotask(()=>{response.emit("data",Buffer.from('{"value":1}'));response.emit("end")})
  }})}}
  const exports={} as {relay:typeof relay}
  const compiled=transpileModule(readFileSync(new URL("./relay.ts",import.meta.url),"utf8"),{compilerOptions:{target:ScriptTarget.ES2022,module:ModuleKind.CommonJS}}).outputText
  new Function("require","exports",compiled)((name:string)=>modules[name]??require(name),exports)
  for(const [method,path,version] of [["GET","v1/scheduler/settings",undefined],["PUT","v1/scheduler/settings",'"7"'],["GET","v1/models",undefined]] as const){
    const response=await exports.relay(new Request(`https://dashboard.test/api/chill/${path}`,{method,headers:{authorization:"Bearer test-token",...(version?{"If-Match":version}:{}),...(method==="PUT"?{"content-type":"application/json"}:{})},...(method==="PUT"?{body:'{"controls":{}}'}:{})}),path.split("/"))
    assert.equal(response.status,200)
    assert.equal(response.headers.get("ETag"),path==="v1/scheduler/settings"?'"8"':null)
    assert.equal(sent.at(-1)!.headers["if-match"],version)
    assert.equal(await response.text(),'{"value":1}')
    if(method==="PUT")assert.equal(sent.at(-1)!.body,'{"controls":{}}')
  }
  tag=undefined
  for(status of [200,409,428]){
    const response=await exports.relay(new Request("https://dashboard.test/api/chill/v1/scheduler/settings",{headers:{authorization:"Bearer test-token"}}),["v1","scheduler","settings"])
    assert.equal(response.status,status)
    assert.equal(response.headers.get("ETag"),null)
    assert.equal(sent.at(-1)!.headers["if-match"],undefined)
  }
})
