import { parseSchedulerJSON } from "./scheduler.ts"

// Captured operational summaries peak at 230,362 logical nodes / depth 11.
// Bound downstream recursive copies as well as this response-local DAG.
const maxExpandedNodes=2_000_000,maxExpandedDepth=64
type Restored={value:unknown;nodes:number;depth:number}

// The dictionary is a response-local DAG: entries only refer to earlier entries.
// Keep shared values shared instead of rebuilding source arrays for every check.
export function restoreMeasurementSummary(value:unknown):unknown {
  if(!value||typeof value!=="object"||Array.isArray(value)||!("readProjection" in value))return value
  const projection=value.readProjection as {version?:unknown;values?:unknown[];shapes?:string[][]}|null
  if(projection?.version!=="measurement-summary.v1"||!Array.isArray(projection.values)||!Array.isArray(projection.shapes))throw new Error("측정 요약 형식 미지원")
  const shapes=projection.shapes
  const restored:Restored[]=[]
  function restore(item:unknown,nesting=1):Restored {
    if(nesting>maxExpandedDepth)throw new Error("측정 요약 확장 한도 초과")
    if(typeof item==="string"&&/^~\d+$/.test(item)) {
      const index=Number(item.slice(1))
      if(!Number.isSafeInteger(index)||index>=restored.length)throw new Error("측정 근거 참조 미확인")
      return restored[index]
    }
    if(!item||typeof item!=="object")return {value:item,nodes:1,depth:1}
    let nodes=1,depth=1
    function child(entry:unknown):unknown {
      const next=restore(entry,nesting+1)
      nodes+=next.nodes
      depth=Math.max(depth,next.depth+1)
      if(nodes>maxExpandedNodes||depth>maxExpandedDepth)throw new Error("측정 요약 확장 한도 초과")
      return next.value
    }
    let value:unknown
    if(Array.isArray(item))value=item.map(child)
    else if("$o" in item&&Object.keys(item).length===1) {
      const fields=item.$o
      if(!Array.isArray(fields)||typeof fields[0]!=="number"||!Number.isInteger(fields[0]))throw new Error("측정 필드 참조 미확인")
      const keys=shapes[fields[0]]
      if(!Array.isArray(keys)||fields.length!==keys.length+1||keys.some(key=>typeof key!=="string"))throw new Error("측정 필드 참조 미확인")
      value=Object.fromEntries(keys.map((key,index)=>[key,child(fields[index+1])]))
    }
    else value=Object.fromEntries(Object.entries(item).map(([key,entry])=>[key,child(entry)]))
    return {value,nodes,depth}
  }
  for(const entry of projection.values)restored.push(typeof entry==="string"?{value:entry,nodes:1,depth:1}:restore(entry))
  const {readProjection,...body}=value
  void readProjection
  return restore(body).value
}

export function parseMeasurementJSON(text:string):unknown {
  return restoreMeasurementSummary(parseSchedulerJSON(text))
}
