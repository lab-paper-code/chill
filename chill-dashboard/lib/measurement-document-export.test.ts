import assert from "node:assert/strict"
import test from "node:test"
import { elements, load } from "./evaluation-author-test-helpers.ts"
import { parseMeasurementDocument } from "./measurement-document.ts"
import * as scheduler from "./scheduler.ts"

test("native request preview and downloaded Blob preserve legal token-like metadata through reimport", async()=>{
  const value=parseMeasurementDocument(JSON.stringify({apiVersion:"edge.dacs.io/v1alpha1",kind:"BatchEnergyOptimization",metadata:{name:"reuse-request",namespace:"lab",labels:{"app.example/tokenizer":"bert"},annotations:{"docs.example/password-policy":"public"}},spec:{activeProtocol:{workload:"service"},producerExtension:{tokenizer:"public-model",zero:0}}}),"lab")
  const {MeasurementRequestSource}=load("../components/measurement-request-source.tsx",{"@/components/ui/button":{Button:"button"},"@/lib/scheduler":scheduler})
  const nodes=elements(MeasurementRequestSource({value}))
  const preview=JSON.parse(nodes.find(node=>node.type==="pre")!.props.children as string)
  assert.equal(preview.metadata.labels["app.example/tokenizer"],"bert")
  assert.equal(preview.spec.producerExtension.tokenizer,"public-model")
  const originalDocument=Object.getOwnPropertyDescriptor(globalThis,"document")
  const originalCreate=URL.createObjectURL,originalRevoke=URL.revokeObjectURL
  let downloaded:Blob|undefined,clicks=0
  Object.defineProperty(globalThis,"document",{configurable:true,value:{createElement:()=>({href:"",download:"",click(){clicks++}})}})
  URL.createObjectURL=blob=>{if(blob instanceof Blob)downloaded=blob;return "blob:local-download"}
  URL.revokeObjectURL=()=>{}
  try {
    assert.equal(clicks,0)
    ;(nodes.find(node=>node.type==="button")!.props.onClick as ()=>void)()
    assert.equal(clicks,1)
    const imported=parseMeasurementDocument(await downloaded!.text(),"lab")
    assert.equal((imported.metadata.labels as Record<string,string>)["app.example/tokenizer"],"bert")
    assert.equal((imported.metadata.annotations as Record<string,string>)["docs.example/password-policy"],"public")
    assert.equal((imported.spec.producerExtension as Record<string,unknown>).tokenizer,"public-model")
    assert.equal((imported.spec.producerExtension as Record<string,unknown>).zero,0)
  } finally {
    URL.createObjectURL=originalCreate;URL.revokeObjectURL=originalRevoke
    if(originalDocument)Object.defineProperty(globalThis,"document",originalDocument)
    else Reflect.deleteProperty(globalThis,"document")
  }
})
